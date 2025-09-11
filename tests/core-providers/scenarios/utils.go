package scenarios

import (
	"context"
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

// Tool definitions for testing
var WeatherToolDefinition = schemas.Tool{
	Type: "function",
	Function: schemas.Function{
		Name:        "get_weather",
		Description: "Get the current weather in a given location",
		Parameters: schemas.FunctionParameters{
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
	},
}

var CalculatorToolDefinition = schemas.Tool{
	Type: "function",
	Function: schemas.Function{
		Name:        "calculate",
		Description: "Perform basic mathematical calculations",
		Parameters: schemas.FunctionParameters{
			Type: "object",
			Properties: map[string]interface{}{
				"expression": map[string]interface{}{
					"type":        "string",
					"description": "The mathematical expression to evaluate, e.g. '2 + 3' or '10 * 5'",
				},
			},
			Required: []string{"expression"},
		},
	},
}

var TimeToolDefinition = schemas.Tool{
	Type: "function",
	Function: schemas.Function{
		Name:        "get_current_time",
		Description: "Get the current time in a specific timezone",
		Parameters: schemas.FunctionParameters{
			Type: "object",
			Properties: map[string]interface{}{
				"timezone": map[string]interface{}{
					"type":        "string",
					"description": "The timezone identifier, e.g. 'America/New_York' or 'UTC'",
				},
			},
			Required: []string{"timezone"},
		},
	},
}

// Test images for testing
const TestImageURL = "https://upload.wikimedia.org/wikipedia/commons/a/a7/Camponotus_flavomarginatus_ant.jpg"
const TestImageBase64 = "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAoDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAb/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCdABmX/9k="

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

func CreateImageMessage(text, imageURL string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: schemas.ChatMessageContent{
			ContentBlocks: &[]schemas.ContentBlock{
				{
					Type: schemas.ContentBlockTypeText,
					Text: bifrost.Ptr(text),
				},
				{
					Type: schemas.ContentBlockTypeImage,
					ImageURL: &schemas.ImageURLStruct{
						URL: imageURL,
					},
				},
			},
		},
	}
}

func CreateToolMessage(content string, toolCallID string) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role: schemas.ModelChatMessageRoleTool,
		Content: schemas.ChatMessageContent{
			ContentStr: bifrost.Ptr(content),
		},
		ToolMessage: &schemas.ToolMessage{
			ToolCallID: &toolCallID,
		},
	}
}

// GetResultContent returns the string content from a BifrostResponse
// It looks through all choices and returns content from the first choice that has any
func GetResultContent(result *schemas.BifrostResponse) string {
	if result == nil || len(result.Choices) == 0 {
		return ""
	}

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
	if override.MaxTokens != nil {
		result.MaxTokens = override.MaxTokens
	}
	if override.Temperature != nil {
		result.Temperature = override.Temperature
	}
	if override.TopP != nil {
		result.TopP = override.TopP
	}
	if override.TopK != nil {
		result.TopK = override.TopK
	}
	if override.FrequencyPenalty != nil {
		result.FrequencyPenalty = override.FrequencyPenalty
	}
	if override.PresencePenalty != nil {
		result.PresencePenalty = override.PresencePenalty
	}
	if override.StopSequences != nil {
		result.StopSequences = override.StopSequences
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
		MaxTokens:         src.MaxTokens,
		Temperature:       src.Temperature,
		TopP:              src.TopP,
		TopK:              src.TopK,
		FrequencyPenalty:  src.FrequencyPenalty,
		PresencePenalty:   src.PresencePenalty,
		StopSequences:     src.StopSequences,
		Tools:             src.Tools,
		ToolChoice:        src.ToolChoice,
		ParallelToolCalls: src.ParallelToolCalls,
		EncodingFormat:    src.EncodingFormat,
		Dimensions:        src.Dimensions,
		User:              src.User,
		ExtraParams:       src.ExtraParams,
	}
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
