// Package config provides comprehensive test account and configuration management for the Bifrost system.
// It implements account functionality for testing purposes, supporting multiple AI providers
// and comprehensive test scenarios.
package config

import (
	"context"
	"fmt"
	"os"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// ProviderOpenAICustom represents the custom OpenAI provider for testing
const ProviderOpenAICustom = schemas.ModelProvider("openai-custom")

// TestScenarios defines the comprehensive test scenarios
type TestScenarios struct {
	TextCompletion        bool
	SimpleChat            bool
	ChatCompletionStream  bool
	MultiTurnConversation bool
	ToolCalls             bool
	MultipleToolCalls     bool
	End2EndToolCalling    bool
	AutomaticFunctionCall bool
	ImageURL              bool
	ImageBase64           bool
	MultipleImages        bool
	CompleteEnd2End       bool
	ProviderSpecific      bool
	SpeechSynthesis       bool // Text-to-speech functionality
	SpeechSynthesisStream bool // Streaming text-to-speech functionality
	Transcription         bool // Speech-to-text functionality
	TranscriptionStream   bool // Streaming speech-to-text functionality
	Embedding             bool // Embedding functionality
}

// ComprehensiveTestConfig extends TestConfig with additional scenarios
type ComprehensiveTestConfig struct {
	Provider             schemas.ModelProvider
	ChatModel            string
	TextModel            string
	EmbeddingModel       string
	TranscriptionModel   string
	SpeechSynthesisModel string
	Scenarios            TestScenarios
	CustomParams         *schemas.ModelParameters
	Fallbacks            []schemas.Fallback
	SkipReason           string // Reason to skip certain tests
}

// ComprehensiveTestAccount provides a test implementation of the Account interface for comprehensive testing.
type ComprehensiveTestAccount struct{}

// getEnvWithDefault returns the value of the environment variable if set, otherwise returns the default value
func getEnvWithDefault(envVar, defaultValue string) string {
	if value := os.Getenv(envVar); value != "" {
		return value
	}
	return defaultValue
}

// GetConfiguredProviders returns the list of initially supported providers.
func (account *ComprehensiveTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{
		schemas.OpenAI,
		schemas.Anthropic,
		schemas.Bedrock,
		schemas.Cohere,
		schemas.Azure,
		schemas.Vertex,
		schemas.Ollama,
		schemas.Mistral,
		schemas.Groq,
		schemas.SGL,
		schemas.Parasail,
		schemas.Cerebras,
		schemas.Gemini,
		schemas.OpenRouter,
		ProviderOpenAICustom,
	}, nil
}

// GetKeysForProvider returns the API keys and associated models for a given provider.
func (account *ComprehensiveTestAccount) GetKeysForProvider(ctx *context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	switch providerKey {
	case schemas.OpenAI:
		return []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		}, nil
	case ProviderOpenAICustom:
		return []schemas.Key{
			{
				Value:  os.Getenv("GROQ_API_KEY"), // Use GROQ API key for OpenAI-compatible endpoint
				Models: []string{},
				Weight: 1.0,
			},
		}, nil
	case schemas.Anthropic:
		return []schemas.Key{
			{
				Value:  os.Getenv("ANTHROPIC_API_KEY"),
				Models: []string{"claude-3-7-sonnet-20250219", "claude-3-5-sonnet-20240620", "claude-2.1"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Bedrock:
		return []schemas.Key{
			{
				Models: []string{"anthropic.claude-v2:1", "mistral.mixtral-8x7b-instruct-v0:1", "mistral.mistral-large-2402-v1:0", "anthropic.claude-3-sonnet-20240229-v1:0"},
				Weight: 1.0,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey:    os.Getenv("AWS_ACCESS_KEY_ID"),
					SecretKey:    os.Getenv("AWS_SECRET_ACCESS_KEY"),
					SessionToken: bifrost.Ptr(os.Getenv("AWS_SESSION_TOKEN")),
					Region:       bifrost.Ptr(getEnvWithDefault("AWS_REGION", "us-east-1")),
				},
			},
		}, nil
	case schemas.Cohere:
		return []schemas.Key{
			{
				Value:  os.Getenv("COHERE_API_KEY"),
				Models: []string{"command-a-03-2025", "c4ai-aya-vision-8b"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Azure:
		return []schemas.Key{
			{
				Value:  os.Getenv("AZURE_API_KEY"),
				Models: []string{"gpt-4o", "text-embedding-3-small"},
				Weight: 1.0,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint: os.Getenv("AZURE_ENDPOINT"),
					Deployments: map[string]string{
						"gpt-4o":                 "gpt-4o-aug",
						"text-embedding-3-small": "text-embedding-3-small-deployment",
					},
					// Use environment variable for API version with fallback to current preview version
					// Note: This is a preview API version that may change over time. Update as needed.
					// Set AZURE_API_VERSION environment variable to override the default.
					APIVersion: bifrost.Ptr(getEnvWithDefault("AZURE_API_VERSION", "2024-08-01-preview")),
				},
			},
		}, nil
	case schemas.Vertex:
		return []schemas.Key{
			{
				Value:  os.Getenv("VERTEX_API_KEY"),
				Models: []string{},
				Weight: 1.0,
				VertexKeyConfig: &schemas.VertexKeyConfig{
					ProjectID:       os.Getenv("VERTEX_PROJECT_ID"),
					Region:          getEnvWithDefault("VERTEX_REGION", "us-central1"),
					AuthCredentials: os.Getenv("VERTEX_CREDENTIALS"),
				},
			},
		}, nil
	case schemas.Mistral:
		return []schemas.Key{
			{
				Value:  os.Getenv("MISTRAL_API_KEY"),
				Models: []string{"mistral-large-2411", "pixtral-12b-latest", "mistral-embed"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Groq:
		return []schemas.Key{
			{
				Value:  os.Getenv("GROQ_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		}, nil
	case schemas.Parasail:
		return []schemas.Key{
			{
				Value:  os.Getenv("PARASAIL_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		}, nil
	case schemas.Cerebras:
		return []schemas.Key{
			{
				Value:  os.Getenv("CEREBRAS_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		}, nil
	case schemas.Gemini:
		return []schemas.Key{
			{
				Value:  os.Getenv("GEMINI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		}, nil
	case schemas.OpenRouter:
		return []schemas.Key{
			{
				Value:  os.Getenv("OPENROUTER_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

// GetConfigForProvider returns the configuration settings for a given provider.
func (account *ComprehensiveTestAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	switch providerKey {
	case schemas.OpenAI:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case ProviderOpenAICustom:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:                        getEnvWithDefault("GROQ_OPENAI_BASE_URL", "https://api.groq.com/openai"),
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
			CustomProviderConfig: &schemas.CustomProviderConfig{
				BaseProviderType: schemas.OpenAI,
				AllowedRequests: &schemas.AllowedRequests{
					TextCompletion:       false,
					ChatCompletion:       true,
					ChatCompletionStream: true,
					Embedding:            false,
					Speech:               false,
					SpeechStream:         false,
					Transcription:        false,
					TranscriptionStream:  false,
				},
			},
		}, nil
	case schemas.Anthropic:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Bedrock:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Cohere:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Azure:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Vertex:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Ollama:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
				BaseURL:                        getEnvWithDefault("OLLAMA_BASE_URL", "http://localhost:11434"),
			},
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Mistral:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Groq:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.SGL:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:                        os.Getenv("SGL_BASE_URL"),
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Parasail:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Cerebras:
		return &schemas.ProviderConfig{
			NetworkConfig:            schemas.DefaultNetworkConfig,
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.Gemini:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	case schemas.OpenRouter:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 60,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

// AllProviderConfigs contains test configurations for all providers
var AllProviderConfigs = []ComprehensiveTestConfig{
	{
		Provider:             schemas.OpenAI,
		ChatModel:            "gpt-4o-mini",
		TextModel:            "", // OpenAI doesn't support text completion in newer models
		TranscriptionModel:   "whisper-1",
		SpeechSynthesisModel: "tts-1",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       true, // OpenAI supports TTS
			SpeechSynthesisStream: true, // OpenAI supports streaming TTS
			Transcription:         true, // OpenAI supports STT with Whisper
			TranscriptionStream:   true, // OpenAI supports streaming STT
			Embedding:             true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.Anthropic, Model: "claude-3-7-sonnet-20250219"},
		},
	},
	{
		Provider:  schemas.Anthropic,
		ChatModel: "claude-3-7-sonnet-20250219",
		TextModel: "", // Anthropic doesn't support text completion
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.Bedrock,
		ChatModel: "anthropic.claude-3-sonnet-20240229-v1:0",
		TextModel: "", // Bedrock Claude doesn't support text completion
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported for Claude
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.Cohere,
		ChatModel: "command-a-03-2025",
		TextModel: "", // Cohere focuses on chat
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not typical for Cohere
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: false, // May not support automatic
			ImageURL:              false, // Check if supported
			ImageBase64:           false, // Check if supported
			MultipleImages:        false, // Check if supported
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.Azure,
		ChatModel: "gpt-4o",
		TextModel: "", // Azure OpenAI doesn't support text completion in newer models
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported yet
			SpeechSynthesisStream: false, // Not supported yet
			Transcription:         false, // Not supported yet
			TranscriptionStream:   false, // Not supported yet
			Embedding:             true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.Vertex,
		ChatModel: "gemini-pro",
		TextModel: "", // Vertex focuses on chat
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not typical
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.Mistral,
		ChatModel: "mistral-large-2411",
		TextModel: "", // Mistral focuses on chat
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not typical
			SimpleChat:            true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.Ollama,
		ChatModel: "llama3.2",
		TextModel: "", // Ollama focuses on chat
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not typical
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.Groq,
		ChatModel: "llama-3.3-70b-versatile",
		TextModel: "", // Groq doesn't support text completion
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  ProviderOpenAICustom,
		ChatModel: "llama-3.3-70b-versatile",
		TextModel: "", // Custom OpenAI instance doesn't support text completion
		Scenarios: TestScenarios{
			TextCompletion:        false,
			SimpleChat:            true, // Enable simple chat for testing
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:             schemas.Gemini,
		ChatModel:            "gemini-2.0-flash",
		TextModel:            "", // GenAI doesn't support text completion in newer models
		TranscriptionModel:   "gemini-2.5-flash",
		SpeechSynthesisModel: "gemini-2.5-flash-preview-tts",
		EmbeddingModel:       "text-embedding-004",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   true,
			Embedding:             true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:  schemas.OpenRouter,
		ChatModel: "openai/gpt-4o",
		TextModel: "google/gemini-2.5-flash",
		Scenarios: TestScenarios{
			TextCompletion:        true,
			SimpleChat:            true,
			ChatCompletionStream:  true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ProviderSpecific:      true,
			SpeechSynthesis:       false,
			SpeechSynthesisStream: false,
			Transcription:         false,
			TranscriptionStream:   false,
			Embedding:             false, 
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
}
