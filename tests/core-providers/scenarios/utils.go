package scenarios

import (
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

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

// Audio test data for speech and transcription testing
// Note: In production tests, you would use actual audio files
// These are minimal valid audio file headers for testing purposes

// TestAudioDataMP3 - Sample MP3 data for testing (MP3 header with minimal audio)
var TestAudioDataMP3 = []byte{
	0xFF, 0xFB, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	// Additional MP3 frames would go here for real audio...
}

// TestAudioDataWAV - Sample WAV data for testing (WAV header with minimal audio)
var TestAudioDataWAV = []byte{
	// WAV header
	0x52, 0x49, 0x46, 0x46, // "RIFF"
	0x24, 0x00, 0x00, 0x00, // File size
	0x57, 0x41, 0x56, 0x45, // "WAVE"
	0x66, 0x6D, 0x74, 0x20, // "fmt "
	0x10, 0x00, 0x00, 0x00, // Format chunk size
	0x01, 0x00, 0x01, 0x00, // Audio format and channels
	0x44, 0xAC, 0x00, 0x00, // Sample rate (44100)
	0x88, 0x58, 0x01, 0x00, // Byte rate
	0x02, 0x00, 0x10, 0x00, // Block align and bits per sample
	0x64, 0x61, 0x74, 0x61, // "data"
	0x00, 0x00, 0x00, 0x00, // Data size
	// Audio data would go here for real audio...
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
func CreateBasicChatMessage(content string) schemas.BifrostMessage {
	return schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleUser,
		Content: schemas.MessageContent{
			ContentStr: bifrost.Ptr(content),
		},
	}
}

func CreateImageMessage(text, imageURL string) schemas.BifrostMessage {
	return schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleUser,
		Content: schemas.MessageContent{
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

func CreateToolMessage(content string, toolCallID string) schemas.BifrostMessage {
	return schemas.BifrostMessage{
		Role: schemas.ModelChatMessageRoleTool,
		Content: schemas.MessageContent{
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
	if override.ExtraParams != nil {
		result.ExtraParams = override.ExtraParams
	}

	return result
}

// copyModelParameters creates a deep copy of a ModelParameters instance
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
		ExtraParams:       src.ExtraParams,
	}
}
