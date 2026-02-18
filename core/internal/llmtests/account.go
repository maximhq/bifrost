// Package llmtests provides comprehensive test account and configuration management for the Bifrost system.
// It implements account functionality for testing purposes, supporting multiple AI providers
// and comprehensive test scenarios.
package llmtests

import (
	"context"
	"fmt"
	"os"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

const Concurrency = 4

// ProviderOpenAICustom represents the custom OpenAI provider for testing
const ProviderOpenAICustom = schemas.ModelProvider("openai-custom")

// TestScenarios defines the comprehensive test scenarios
type TestScenarios struct {
	TextCompletion         bool
	TextCompletionStream   bool
	SimpleChat             bool
	CompletionStream       bool
	MultiTurnConversation  bool
	ToolCalls              bool
	ToolCallsStreaming     bool // Streaming tool calls functionality
	MultipleToolCalls      bool
	End2EndToolCalling     bool
	AutomaticFunctionCall  bool
	ImageURL               bool
	ImageBase64            bool
	MultipleImages         bool
	FileBase64             bool
	FileURL                bool
	CompleteEnd2End        bool
	SpeechSynthesis        bool // Text-to-speech functionality
	SpeechSynthesisStream  bool // Streaming text-to-speech functionality
	Transcription          bool // Speech-to-text functionality
	TranscriptionStream    bool // Streaming speech-to-text functionality
	Embedding              bool // Embedding functionality
	Reasoning              bool // Reasoning/thinking functionality via Responses API
	PromptCaching          bool // Prompt caching functionality
	ListModels             bool // List available models functionality
	ImageGeneration        bool // Image generation functionality
	ImageGenerationStream  bool // Streaming image generation functionality
	ImageEdit              bool // Image edit functionality
	ImageEditStream        bool // Streaming image edit functionality
	ImageVariation         bool // Image variation functionality
	ImageVariationStream   bool // Streaming image variation functionality (if supported)
	BatchCreate            bool // Batch API create functionality
	BatchList              bool // Batch API list functionality
	BatchRetrieve          bool // Batch API retrieve functionality
	BatchCancel            bool // Batch API cancel functionality
	BatchResults           bool // Batch API results functionality
	FileUpload             bool // File API upload functionality
	FileList               bool // File API list functionality
	FileRetrieve           bool // File API retrieve functionality
	FileDelete             bool // File API delete functionality
	FileContent            bool // File API content download functionality
	FileBatchInput         bool // Whether batch create supports file-based input (InputFileID)
	CountTokens            bool // Count tokens functionality
	ChatAudio              bool // Chat completion with audio input/output functionality
	StructuredOutputs      bool // Structured outputs (JSON schema) functionality
	WebSearchTool          bool // Web search tool functionality
	ContainerCreate        bool // Container API create functionality
	ContainerList          bool // Container API list functionality
	ContainerRetrieve      bool // Container API retrieve functionality
	ContainerDelete        bool // Container API delete functionality
	ContainerFileCreate    bool // Container File API create functionality
	ContainerFileList      bool // Container File API list functionality
	ContainerFileRetrieve  bool // Container File API retrieve functionality
	ContainerFileContent   bool // Container File API content functionality
	ContainerFileDelete    bool // Container File API delete functionality
	PassThroughExtraParams bool // Pass through extra params functionality
	Rerank                 bool // Rerank functionality
}

// ComprehensiveTestConfig extends TestConfig with additional scenarios
type ComprehensiveTestConfig struct {
	Provider                 schemas.ModelProvider
	TextModel                string
	ChatModel                string
	PromptCachingModel       string
	VisionModel              string
	ReasoningModel           string
	EmbeddingModel           string
	RerankModel              string
	TranscriptionModel       string
	SpeechSynthesisModel     string
	ChatAudioModel           string
	Scenarios                TestScenarios
	Fallbacks                []schemas.Fallback     // for chat, responses, image and reasoning tests
	TextCompletionFallbacks  []schemas.Fallback     // for text completion tests
	TranscriptionFallbacks   []schemas.Fallback     // for transcription tests
	SpeechSynthesisFallbacks []schemas.Fallback     // for speech synthesis tests
	EmbeddingFallbacks       []schemas.Fallback     // for embedding tests
	RerankFallbacks          []schemas.Fallback     // for rerank tests
	SkipReason               string                 // Reason to skip certain tests
	ImageGenerationModel     string                 // Model for image generation
	ImageGenerationFallbacks []schemas.Fallback     // Fallbacks for image generation
	ImageEditModel           string                 // Model for image editing
	ImageEditFallbacks       []schemas.Fallback     // Fallbacks for image editing
	ImageVariationModel      string                 // Model for image variation
	ImageVariationFallbacks  []schemas.Fallback     // Fallbacks for image variation
	ExternalTTSProvider      schemas.ModelProvider  // External TTS provider to use for testing
	ExternalTTSModel         string                 // External TTS model to use for testing
	BatchExtraParams         map[string]interface{} // Extra params for batch operations (e.g., role_arn, output_s3_uri for Bedrock)
	FileExtraParams          map[string]interface{} // Extra params for file operations (e.g., s3_bucket for Bedrock)
	DisableParallelFor       []string               // Test scenarios to disable parallel execution for (e.g., "Transcription" for rate-limited APIs)
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
		schemas.Elevenlabs,
		schemas.Perplexity,
		schemas.Cerebras,
		schemas.Gemini,
		schemas.OpenRouter,
		schemas.HuggingFace,
		schemas.Nebius,
		schemas.XAI,
		schemas.Replicate,
		ProviderOpenAICustom,
	}, nil
}

// GetKeysForProvider returns the API keys and associated models for a given provider.
func (account *ComprehensiveTestAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	switch providerKey {
	case schemas.OpenAI:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.OPENAI_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case ProviderOpenAICustom:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.OPENAI_API_KEY"), // Use GROQ API key for OpenAI-compatible endpoint
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Anthropic:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.ANTHROPIC_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Bedrock:
		return []schemas.Key{
			{
				Models: []string{},
				Weight: 1.0,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey:    *schemas.NewEnvVar("env.AWS_ACCESS_KEY_ID"),
					SecretKey:    *schemas.NewEnvVar("env.AWS_SECRET_ACCESS_KEY"),
					SessionToken: schemas.NewEnvVar("env.AWS_SESSION_TOKEN"),
					Region:       schemas.NewEnvVar(getEnvWithDefault("AWS_REGION", "us-east-1")),
					ARN:          schemas.NewEnvVar("env.AWS_ARN"),
					Deployments: map[string]string{
						"claude-3.7-sonnet": "us.anthropic.claude-3-7-sonnet-20250219-v1:0",
						"claude-4-sonnet":   "global.anthropic.claude-sonnet-4-20250514-v1:0",
						"claude-4.5-sonnet": "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
						"claude-4.5-haiku":  "global.anthropic.claude-haiku-4-5-20251001-v1:0",
					},
				},
			},
			{
				Models: []string{},
				Weight: 1.0,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey:    *schemas.NewEnvVar("env.AWS_ACCESS_KEY_ID"),
					SecretKey:    *schemas.NewEnvVar("env.AWS_SECRET_ACCESS_KEY"),
					SessionToken: schemas.NewEnvVar("env.AWS_SESSION_TOKEN"),
					Region:       schemas.NewEnvVar(getEnvWithDefault("AWS_REGION", "us-east-1")),
					ARN:          schemas.NewEnvVar("env.AWS_BEDROCK_ARN"),
					Deployments: map[string]string{
						"claude-3.5-sonnet": "anthropic.claude-3-5-sonnet-20240620-v1:0",
						"claude-3.7-sonnet": "us.anthropic.claude-3-7-sonnet-20250219-v1:0",
						"claude-4-sonnet":   "global.anthropic.claude-sonnet-4-20250514-v1:0",
						"claude-4.5-sonnet": "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
						"claude-4.5-haiku":  "global.anthropic.claude-haiku-4-5-20251001-v1:0",
					},
				},
				UseForBatchAPI: bifrost.Ptr(true),
			},
			{
				Models: []string{"cohere.embed-v4:0", "amazon.nova-canvas-v1:0"},
				Weight: 1.0,
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey:    *schemas.NewEnvVar("env.AWS_ACCESS_KEY_ID"),
					SecretKey:    *schemas.NewEnvVar("env.AWS_SECRET_ACCESS_KEY"),
					SessionToken: schemas.NewEnvVar("env.AWS_SESSION_TOKEN"),
					Region:       schemas.NewEnvVar(getEnvWithDefault("AWS_REGION", "us-east-1")),
				},
			},
		}, nil
	case schemas.Cohere:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.COHERE_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Azure:
		return []schemas.Key{
			{
				Value:  *schemas.NewEnvVar("env.AZURE_API_KEY"),
				Models: []string{},
				Weight: 1.0,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("env.AZURE_ENDPOINT"),
					APIVersion: schemas.NewEnvVar("env.AZURE_API_VERSION"),
					Deployments: map[string]string{
						"gpt-4o":                 "gpt-4o",
						"gpt-4o-backup":          "gpt-4o-3",
						"claude-opus-4-5":        "claude-opus-4-5",
						"o1":                     "o1",
						"gpt-image-1":            "gpt-image-1",
						"text-embedding-ada-002": "text-embedding-ada-002",
					},
					ClientID:     schemas.NewEnvVar("env.AZURE_CLIENT_ID"),
					ClientSecret: schemas.NewEnvVar("env.AZURE_CLIENT_SECRET"),
					TenantID:     schemas.NewEnvVar("env.AZURE_TENANT_ID"),
				},
				UseForBatchAPI: bifrost.Ptr(true),
			},
			{
				Value:  *schemas.NewEnvVar("env.AZURE_API_KEY"),
				Models: []string{},
				Weight: 1.0,
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint:   *schemas.NewEnvVar("env.AZURE_ENDPOINT"),
					APIVersion: schemas.NewEnvVar("env.AZURE_API_VERSION"),
					Deployments: map[string]string{
						"whisper":                   "whisper",
						"gpt-4o-mini-tts":           "gpt-4o-mini-tts",
						"gpt-4o-mini-audio-preview": "gpt-4o-mini-audio-preview",
					},
				},
			},
		}, nil
	case schemas.Vertex:
		return []schemas.Key{
			{
				Value:  *schemas.NewEnvVar("env.VERTEX_API_KEY"),
				Models: []string{"text-multilingual-embedding-002", "google/gemini-2.0-flash-001", "gemini-2.5-flash-image", "imagen-4.0-generate-001", "imagen-3.0-capability-001"},
				Weight: 1.0,
				VertexKeyConfig: &schemas.VertexKeyConfig{
					ProjectID:       *schemas.NewEnvVar("env.VERTEX_PROJECT_ID"),
					Region:          *schemas.NewEnvVar(getEnvWithDefault("VERTEX_REGION", "us-central1")),
					AuthCredentials: *schemas.NewEnvVar("env.VERTEX_CREDENTIALS"),
				},
				UseForBatchAPI: bifrost.Ptr(true),
			},
			{
				Value:  *schemas.NewEnvVar("env.VERTEX_API_KEY"),
				Models: []string{"claude-sonnet-4-5", "claude-4.5-haiku", "claude-opus-4-5"},
				Weight: 1.0,
				VertexKeyConfig: &schemas.VertexKeyConfig{
					ProjectID:       *schemas.NewEnvVar("env.VERTEX_PROJECT_ID"),
					Region:          *schemas.NewEnvVar(getEnvWithDefault("VERTEX_REGION_ANTHROPIC", "us-east5")),
					AuthCredentials: *schemas.NewEnvVar("env.VERTEX_CREDENTIALS"),
					Deployments: map[string]string{
						"claude-sonnet-4-5": "claude-sonnet-4-5",
						"claude-4.5-haiku":  "claude-haiku-4-5@20251001",
						"claude-opus-4-5":   "claude-opus-4-5",
					},
				},
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Mistral:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.MISTRAL_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Groq:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.GROQ_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Parasail:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.PARASAIL_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Elevenlabs:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.ELEVENLABS_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Perplexity:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.PERPLEXITY_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Cerebras:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.CEREBRAS_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Gemini:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.GEMINI_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.OpenRouter:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.OPENROUTER_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.HuggingFace:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.HUGGING_FACE_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Nebius:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.NEBIUS_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.XAI:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.XAI_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
			},
		}, nil
	case schemas.Replicate:
		return []schemas.Key{
			{
				Value:          *schemas.NewEnvVar("env.REPLICATE_API_KEY"),
				Models:         []string{},
				Weight:         1.0,
				UseForBatchAPI: bifrost.Ptr(true),
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
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Higher retries for production-grade provider
				RetryBackoffInitial:            500 * time.Millisecond,
				RetryBackoffMax:                8 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 10,
				BufferSize:  10,
			},
		}, nil
	case ProviderOpenAICustom:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:                        "https://api.openai.com",
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Higher retries for Groq (can be flaky)
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                10 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
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
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Claude is generally reliable
				RetryBackoffInitial:            500 * time.Millisecond,
				RetryBackoffMax:                8 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Bedrock:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // AWS services can have occasional issues
				RetryBackoffInitial:            5 * time.Second,
				RetryBackoffMax:                40 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Cohere:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Cohere can be variable
				RetryBackoffInitial:            5 * time.Second,
				RetryBackoffMax:                40 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Azure:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 600,
				MaxRetries:                     10,
				RetryBackoffInitial:            20 * time.Second,
				RetryBackoffMax:                3 * time.Minute,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Vertex:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Google Cloud is generally reliable
				RetryBackoffInitial:            500 * time.Millisecond,
				RetryBackoffMax:                8 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Ollama:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     8, // Local service, fewer retries needed
				RetryBackoffInitial:            250 * time.Millisecond,
				RetryBackoffMax:                4 * time.Second,
				BaseURL:                        os.Getenv("OLLAMA_BASE_URL"),
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Mistral:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Mistral can be variable
				RetryBackoffInitial:            5 * time.Second,
				RetryBackoffMax:                5 * time.Minute,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Groq:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Groq can be flaky at times
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                15 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.SGL:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:                        os.Getenv("SGL_BASE_URL"),
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // SGL (self-hosted) can be variable
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                15 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Parasail:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Parasail can be variable
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Elevenlabs:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Elevenlabs can be variable
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Perplexity:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Perplexity can be variable
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Cerebras:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Cerebras is reasonably stable
				RetryBackoffInitial:            5 * time.Second,
				RetryBackoffMax:                3 * time.Minute,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Gemini:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // Gemini can be variable
				RetryBackoffInitial:            750 * time.Millisecond,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  20,
			},
		}, nil
	case schemas.OpenRouter:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10, // OpenRouter can be variable (proxy service)
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.HuggingFace:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 300,
				MaxRetries:                     10, // HuggingFace can be variable
				RetryBackoffInitial:            2 * time.Second,
				RetryBackoffMax:                30 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Nebius:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10,
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.XAI:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 120,
				MaxRetries:                     10,
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
		}, nil
	case schemas.Replicate:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 300,
				MaxRetries:                     10,
				RetryBackoffInitial:            1 * time.Second,
				RetryBackoffMax:                12 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: Concurrency,
				BufferSize:  10,
			},
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
		TextModel:            "",        // OpenAI doesn't support text completion in newer models
		ReasoningModel:       "o1-mini", // OpenAI reasoning model
		PromptCachingModel:   "gpt-4.1",
		TranscriptionModel:   "whisper-1",
		SpeechSynthesisModel: "tts-1",
		ImageGenerationModel: "gpt-image-1",
		ImageEditModel:       "dall-e-2",
		ImageVariationModel:  "dall-e-2",
		ChatAudioModel:       "gpt-4o-mini-audio-preview",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			TextCompletionStream:  false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       true,  // OpenAI supports TTS
			SpeechSynthesisStream: true,  // OpenAI supports streaming TTS
			Transcription:         true,  // OpenAI supports STT with Whisper
			TranscriptionStream:   true,  // OpenAI supports streaming STT
			ImageGeneration:       true,  // OpenAI supports image generation with DALL-E
			ImageGenerationStream: true,  // OpenAI supports streaming image generation
			ImageEdit:             true,  // OpenAI supports image editing
			ImageEditStream:       true,  // OpenAI supports streaming image editing
			ImageVariation:        true,  // OpenAI supports image variation
			ImageVariationStream:  false, // OpenAI does not support streaming image variation
			Embedding:             true,
			Reasoning:             true, // OpenAI supports reasoning via o1 models
			ListModels:            true,
			BatchCreate:           true, // OpenAI supports batch API
			BatchList:             true, // OpenAI supports batch API
			BatchRetrieve:         true, // OpenAI supports batch API
			BatchCancel:           true, // OpenAI supports batch API
			BatchResults:          true, // OpenAI supports batch API
			FileUpload:            true, // OpenAI supports file API
			FileList:              true, // OpenAI supports file API
			FileRetrieve:          true, // OpenAI supports file API
			FileDelete:            true, // OpenAI supports file API
			FileContent:           true, // OpenAI supports file API
			ChatAudio:             true, // OpenAI supports chat audio
			ContainerCreate:       true, // OpenAI supports container API
			ContainerList:         true, // OpenAI supports container API
			ContainerRetrieve:     true, // OpenAI supports container API
			ContainerDelete:       true, // OpenAI supports container API
			ContainerFileCreate:   true, // OpenAI supports container file API
			ContainerFileList:     true, // OpenAI supports container file API
			ContainerFileRetrieve: true, // OpenAI supports container file API
			ContainerFileContent:  true, // OpenAI supports container file API
			ContainerFileDelete:   true, // OpenAI supports container file API
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
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			PromptCaching:         true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
			ImageGeneration:       false,
			ImageGenerationStream: false,
			ImageEdit:             false, // Anthropic does not support image editing
			ImageEditStream:       false, // Anthropic does not support streaming image editing
			ImageVariation:        false, // Anthropic does not support image variation
			ImageVariationStream:  false, // Anthropic does not support streaming image variation
			ListModels:            true,
			BatchCreate:           true, // Anthropic supports batch API
			BatchList:             true, // Anthropic supports batch API
			BatchRetrieve:         true, // Anthropic supports batch API
			BatchCancel:           true, // Anthropic supports batch API
			BatchResults:          true, // Anthropic supports batch API
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:            schemas.Bedrock,
		ChatModel:           "anthropic.claude-3-sonnet-20240229-v1:0",
		TextModel:           "", // Bedrock Claude doesn't support text completion
		ImageEditModel:      "amazon.titan-image-generator-v1",
		ImageVariationModel: "amazon.titan-image-generator-v1",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported for Claude
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			PromptCaching:         true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             true,
			ImageGeneration:       false,
			ImageGenerationStream: false,
			ImageEdit:             true,  // Bedrock supports image editing
			ImageEditStream:       false, // Bedrock does not support streaming image editing
			ImageVariation:        true,  // Bedrock supports image variation
			ImageVariationStream:  false, // Bedrock does not support streaming image variation
			ListModels:            true,
			BatchCreate:           true, // Bedrock supports batch via Model Invocation Jobs (requires S3 config)
			BatchList:             true, // Bedrock supports listing batch jobs
			BatchRetrieve:         true, // Bedrock supports retrieving batch jobs
			BatchCancel:           true, // Bedrock supports stopping batch jobs
			BatchResults:          true, // Bedrock batch results via S3
			FileUpload:            true, // Bedrock file upload to S3 (requires S3 config)
			FileList:              true, // Bedrock file list from S3 (requires S3 config)
			FileRetrieve:          true, // Bedrock file retrieve from S3 (requires S3 config)
			FileDelete:            true, // Bedrock file delete from S3 (requires S3 config)
			FileContent:           true, // Bedrock file content from S3 (requires S3 config)
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
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: false, // May not support automatic
			ImageURL:              false, // Check if supported
			ImageBase64:           false, // Check if supported
			MultipleImages:        false, // Check if supported
			CompleteEnd2End:       true,
			ImageGeneration:       false,
			ImageGenerationStream: false,
			ImageEdit:             false, // Cohere does not support image editing
			ImageEditStream:       false, // Cohere does not support streaming image editing
			ImageVariation:        false, // Cohere does not support image variation
			ImageVariationStream:  false, // Cohere does not support streaming image variation
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             true,
			ListModels:            true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:             schemas.Azure,
		ChatModel:            "gpt-4o",
		TextModel:            "", // Azure doesn't support text completion in newer models
		ChatAudioModel:       "gpt-4o-mini-audio-preview",
		TranscriptionModel:   "whisper-1",
		SpeechSynthesisModel: "gpt-4o-mini-tts",
		ImageGenerationModel: "gpt-image-1",
		ImageEditModel:       "dall-e-2",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       true,  // Supported via gpt-4o-mini-tts
			SpeechSynthesisStream: true,  // Supported via gpt-4o-mini-tts
			Transcription:         true,  // Supported via whisper-1
			TranscriptionStream:   false, // Not properly supported yet by Azure
			Embedding:             true,
			ImageGeneration:       false, // Skipped for Azure
			ImageGenerationStream: false, // Skipped for Azure
			ImageEdit:             true,  // Azure supports image editing
			ImageEditStream:       true,  // Azure supports streaming image editing
			ImageVariation:        false, // Azure does not support image variation
			ImageVariationStream:  false, // Azure does not support streaming image variation
			ListModels:            true,
			BatchCreate:           true, // Azure supports batch API
			BatchList:             true, // Azure supports batch API
			BatchRetrieve:         true, // Azure supports batch API
			BatchCancel:           true, // Azure supports batch API
			BatchResults:          true, // Azure supports batch API
			FileUpload:            true, // Azure supports file API
			FileList:              true, // Azure supports file API
			FileRetrieve:          true, // Azure supports file API
			FileDelete:            true, // Azure supports file API
			FileContent:           true, // Azure supports file API
			ChatAudio:             true, // Azure supports chat audio
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
		DisableParallelFor: []string{"Transcription"}, // Azure Whisper has 3 calls/minute quota
	},
	{
		Provider:             schemas.Vertex,
		ChatModel:            "gemini-pro",
		TextModel:            "", // Vertex focuses on chat
		ImageGenerationModel: "imagen-4.0-generate-001",
		ImageEditModel:       "imagen-4.0-generate-001",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not typical
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ImageGeneration:       true,
			ImageGenerationStream: false,
			ImageEdit:             true,  // Vertex supports image editing
			ImageEditStream:       false, // Vertex does not support streaming image editing
			ImageVariation:        false, // Vertex does not support image variation
			ImageVariationStream:  false, // Vertex does not support streaming image variation
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             true,
			ListModels:            true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:           schemas.Mistral,
		ChatModel:          "mistral-large-2411",
		TextModel:          "", // Mistral focuses on chat
		TranscriptionModel: "voxtral-mini-latest",
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
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         true,  // Supported via voxtral-mini-latest
			TranscriptionStream:   true,  // Supported via voxtral-mini-latest
			Embedding:             true,
			ImageGeneration:       false,
			ImageGenerationStream: false,
			ImageEdit:             false, // Mistral does not support image editing
			ImageEditStream:       false, // Mistral does not support streaming image editing
			ImageVariation:        false, // Mistral does not support image variation
			ImageVariationStream:  false, // Mistral does not support streaming image variation
			ListModels:            true,
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
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
			ImageGeneration:       false,
			ImageGenerationStream: false,
			ImageEdit:             false, // Ollama does not support image editing
			ImageEditStream:       false, // Ollama does not support streaming image editing
			ImageVariation:        false, // Ollama does not support image variation
			ImageVariationStream:  false, // Ollama does not support streaming image variation
			ListModels:            true,
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
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
			ImageGeneration:       false,
			ImageGenerationStream: false,
			ImageEdit:             false, // Groq does not support image editing
			ImageEditStream:       false, // Groq does not support streaming image editing
			ImageVariation:        false, // Groq does not support image variation
			ImageVariationStream:  false, // Groq does not support streaming image variation
			ListModels:            true,
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
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              false,
			ImageBase64:           false,
			MultipleImages:        false,
			CompleteEnd2End:       true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false,
			ImageGeneration:       false, // ProviderOpenAICustom does not support image generation
			ImageGenerationStream: false, // ProviderOpenAICustom does not support streaming image generation
			ImageEdit:             false, // ProviderOpenAICustom does not support image editing
			ImageEditStream:       false, // ProviderOpenAICustom does not support streaming image editing
			ImageVariation:        false, // ProviderOpenAICustom does not support image variation
			ImageVariationStream:  false, // ProviderOpenAICustom does not support streaming image variation
			ListModels:            true,
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
		EmbeddingModel:       "gemini-embedding-001",
		ImageGenerationModel: "imagen-4.0-generate-001",
		ImageEditModel:       "imagen-4.0-generate-001",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not supported
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: true,
			Transcription:         true,
			TranscriptionStream:   true,
			Embedding:             true,
			ImageGeneration:       true,
			ImageGenerationStream: false,
			ImageEdit:             true,  // Gemini supports image editing
			ImageEditStream:       false, // Gemini does not support streaming image editing
			ImageVariation:        false, // Gemini does not support image variation
			ImageVariationStream:  false, // Gemini does not support streaming image variation
			ListModels:            true,
			BatchCreate:           true,
			BatchList:             true,
			BatchRetrieve:         true,
			BatchCancel:           true,
			BatchResults:          true,
			FileUpload:            true,
			FileList:              true,
			FileRetrieve:          true,
			FileDelete:            true,
			FileContent:           false, // Gemini doesn't support direct content download
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
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			ImageGeneration:       false,
			ImageGenerationStream: false,
			ImageEdit:             false, // OpenRouter does not support image editing
			ImageEditStream:       false, // OpenRouter does not support streaming image editing
			ImageVariation:        false, // OpenRouter does not support image variation
			ImageVariationStream:  false, // OpenRouter does not support streaming image variation
			SpeechSynthesis:       false,
			SpeechSynthesisStream: false,
			Transcription:         false,
			TranscriptionStream:   false,
			Embedding:             false,
			ListModels:            true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:             schemas.HuggingFace,
		ChatModel:            "groq/openai/gpt-oss-120b",
		VisionModel:          "novita/zai-org/GLM-4.6V-Flash",
		EmbeddingModel:       "sambanova/intfloat/e5-mistral-7b-instruct",
		TranscriptionModel:   "fal-ai/openai/whisper-large-v3",
		SpeechSynthesisModel: "fal-ai/hexgrad/Kokoro-82M",
		ImageGenerationModel: "fal-ai/fal-ai/flux-2",
		ImageEditModel:       "fal-ai/fal-ai/flux-2",
		Scenarios: TestScenarios{
			TextCompletion:        false,
			TextCompletionStream:  false,
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			ToolCallsStreaming:    true,
			MultipleToolCalls:     false,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			Embedding:             true,
			ImageGeneration:       true,
			ImageGenerationStream: true,
			ImageEdit:             true,  // HuggingFace (fal-ai) supports image editing
			ImageEditStream:       true,  // HuggingFace (fal-ai) supports streaming image editing
			ImageVariation:        false, // HuggingFace does not support image variation
			ImageVariationStream:  false, // HuggingFace does not support streaming image variation
			Transcription:         true,
			TranscriptionStream:   false,
			SpeechSynthesis:       true,
			SpeechSynthesisStream: false,
			Reasoning:             false,
			ListModels:            true,
		},
		Fallbacks: []schemas.Fallback{
			{Provider: schemas.OpenAI, Model: "gpt-4o-mini"},
		},
	},
	{
		Provider:             schemas.XAI,
		ChatModel:            "grok-4-0709",
		TextModel:            "", // XAI focuses on chat
		ImageGenerationModel: "grok-2-image",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not typical
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false, // Not supported
			ImageGeneration:       true,
			ImageGenerationStream: false,
			ImageEdit:             false, // XAI does not support image editing
			ImageEditStream:       false, // XAI does not support streaming image editing
			ImageVariation:        false, // XAI does not support image variation
			ImageVariationStream:  false, // XAI does not support streaming image variation
			ListModels:            true,
		},
	},
	{
		Provider:             schemas.Replicate,
		ChatModel:            "openai/gpt-4.1-mini",
		TextModel:            "openai/gpt-4.1-mini",
		ImageGenerationModel: "black-forest-labs/flux-dev",
		Scenarios: TestScenarios{
			TextCompletion:        false, // Not typical
			SimpleChat:            true,
			CompletionStream:      true,
			MultiTurnConversation: true,
			ToolCalls:             true,
			MultipleToolCalls:     true,
			End2EndToolCalling:    true,
			AutomaticFunctionCall: true,
			ImageURL:              true,
			ImageBase64:           true,
			MultipleImages:        true,
			CompleteEnd2End:       true,
			SpeechSynthesis:       false, // Not supported
			SpeechSynthesisStream: false, // Not supported
			Transcription:         false, // Not supported
			TranscriptionStream:   false, // Not supported
			Embedding:             false, // Not supported
			ListModels:            true,
			ImageGeneration:       true,
			ImageGenerationStream: false,
		},
	},
}
