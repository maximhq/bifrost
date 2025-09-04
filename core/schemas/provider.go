// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"
	"maps"
	"time"
)

const (
	DefaultMaxRetries              = 0
	DefaultRetryBackoffInitial     = 500 * time.Millisecond
	DefaultRetryBackoffMax         = 5 * time.Second
	DefaultRequestTimeoutInSeconds = 30
	DefaultBufferSize              = 100
	DefaultConcurrency             = 10
	DefaultStreamBufferSize        = 100
)

// Pre-defined errors for provider operations
const (
	ErrProviderRequest           = "failed to make HTTP request to provider API"
	ErrProviderResponseUnmarshal = "failed to unmarshal response from provider API"
	ErrProviderJSONMarshaling    = "failed to marshal request body to JSON"
	ErrProviderDecodeStructured  = "failed to decode provider's structured response"
	ErrProviderDecodeRaw         = "failed to decode provider's raw response"
	ErrProviderDecompress        = "failed to decompress provider's response"
)

// NetworkConfig represents the network configuration for provider connections.
// ExtraHeaders is automatically copied during provider initialization to prevent data races.
type NetworkConfig struct {
	// BaseURL is supported for OpenAI, Anthropic, Cohere, Mistral, and Ollama providers (required for Ollama)
	BaseURL                        string            `json:"base_url,omitempty"`                 // Base URL for the provider (optional)
	ExtraHeaders                   map[string]string `json:"extra_headers,omitempty"`            // Additional headers to include in requests (optional)
	DefaultRequestTimeoutInSeconds int               `json:"default_request_timeout_in_seconds"` // Default timeout for requests
	MaxRetries                     int               `json:"max_retries"`                        // Maximum number of retries
	RetryBackoffInitial            time.Duration     `json:"retry_backoff_initial"`              // Initial backoff duration
	RetryBackoffMax                time.Duration     `json:"retry_backoff_max"`                  // Maximum backoff duration
}

// DefaultNetworkConfig is the default network configuration for provider connections.
var DefaultNetworkConfig = NetworkConfig{
	DefaultRequestTimeoutInSeconds: DefaultRequestTimeoutInSeconds,
	MaxRetries:                     DefaultMaxRetries,
	RetryBackoffInitial:            DefaultRetryBackoffInitial,
	RetryBackoffMax:                DefaultRetryBackoffMax,
}

// ConcurrencyAndBufferSize represents configuration for concurrent operations and buffer sizes.
type ConcurrencyAndBufferSize struct {
	Concurrency int `json:"concurrency"` // Number of concurrent operations. Also used as the initial pool size for the provider reponses.
	BufferSize  int `json:"buffer_size"` // Size of the buffer
}

// DefaultConcurrencyAndBufferSize is the default concurrency and buffer size for provider operations.
var DefaultConcurrencyAndBufferSize = ConcurrencyAndBufferSize{
	Concurrency: DefaultConcurrency,
	BufferSize:  DefaultBufferSize,
}

// ProxyType defines the type of proxy to use for connections.
type ProxyType string

const (
	// NoProxy indicates no proxy should be used
	NoProxy ProxyType = "none"
	// HttpProxy indicates an HTTP proxy should be used
	HttpProxy ProxyType = "http"
	// Socks5Proxy indicates a SOCKS5 proxy should be used
	Socks5Proxy ProxyType = "socks5"
	// EnvProxy indicates the proxy should be read from environment variables
	EnvProxy ProxyType = "environment"
)

// ProxyConfig holds the configuration for proxy settings.
type ProxyConfig struct {
	Type     ProxyType `json:"type"`     // Type of proxy to use
	URL      string    `json:"url"`      // URL of the proxy server
	Username string    `json:"username"` // Username for proxy authentication
	Password string    `json:"password"` // Password for proxy authentication
}

// AllowedRequests controls which operations are permitted.
// A nil *AllowedRequests means "all operations allowed."
// A non-nil value only allows fields set to true; omitted or false fields are disallowed.
type AllowedRequests struct {
	TextCompletion       bool `json:"text_completion"`
	ChatCompletion       bool `json:"chat_completion"`
	ChatCompletionStream bool `json:"chat_completion_stream"`
	Embedding            bool `json:"embedding"`
	Speech               bool `json:"speech"`
	SpeechStream         bool `json:"speech_stream"`
	Transcription        bool `json:"transcription"`
	TranscriptionStream  bool `json:"transcription_stream"`
}

// IsOperationAllowed checks if a specific operation is allowed
func (ar *AllowedRequests) IsOperationAllowed(operation Operation) bool {
	if ar == nil {
		return true // Default to allowed if no restrictions
	}

	switch operation {
	case OperationTextCompletion:
		return ar.TextCompletion
	case OperationChatCompletion:
		return ar.ChatCompletion
	case OperationChatCompletionStream:
		return ar.ChatCompletionStream
	case OperationEmbedding:
		return ar.Embedding
	case OperationSpeech:
		return ar.Speech
	case OperationSpeechStream:
		return ar.SpeechStream
	case OperationTranscription:
		return ar.Transcription
	case OperationTranscriptionStream:
		return ar.TranscriptionStream
	default:
		return false // Default to not allowed for unknown operations
	}
}

// GetProviderCapabilities returns the capabilities for a given provider
func GetProviderCapabilities(provider ModelProvider) AllowedRequests {
	switch provider {
	case OpenAI:
		return AllowedRequests{
			TextCompletion:       false,
			ChatCompletion:       true,
			ChatCompletionStream: true,
			Embedding:            true,
			Speech:               true,
			SpeechStream:         true,
			Transcription:        true,
			TranscriptionStream:  true,
		}
	case Anthropic:
		return AllowedRequests{
			TextCompletion:       true,
			ChatCompletion:       true,
			ChatCompletionStream: true,
			Embedding:            false,
			Speech:               false,
			SpeechStream:         false,
			Transcription:        false,
			TranscriptionStream:  false,
		}
	case Cohere:
		return AllowedRequests{
			TextCompletion:       false,
			ChatCompletion:       true,
			ChatCompletionStream: true,
			Embedding:            true,
			Speech:               false,
			SpeechStream:         false,
			Transcription:        false,
			TranscriptionStream:  false,
		}
	case Bedrock:
		return AllowedRequests{
			TextCompletion:       true,
			ChatCompletion:       true,
			ChatCompletionStream: true,
			Embedding:            true,
			Speech:               false,
			SpeechStream:         false,
			Transcription:        false,
			TranscriptionStream:  false,
		}
	case Gemini:
		return AllowedRequests{
			TextCompletion:       false,
			ChatCompletion:       true,
			ChatCompletionStream: true,
			Embedding:            true,
			Speech:               true,
			SpeechStream:         true,
			Transcription:        true,
			TranscriptionStream:  true,
		}
	default:
		 // Unknown providers: safest default is no capabilities; upstream validation should reject these.
		return AllowedRequests{}
	}
}

// GetDefaultAllowedRequestsForProvider returns a default AllowedRequests based on provider capabilities
// This ensures that when no explicit restrictions are set, operations are limited to what the provider supports
func GetDefaultAllowedRequestsForProvider(baseProvider ModelProvider) *AllowedRequests {
	capabilities := GetProviderCapabilities(baseProvider)
	return &capabilities
}

// ValidateAllowedRequests checks if the allowed requests are valid for the given base provider
// and silently sets any unsupported operations to false instead of returning an error
func ValidateAllowedRequests(allowedRequests *AllowedRequests, baseProvider ModelProvider) {
	if allowedRequests == nil {
		// nil means all operations allowed, which is always valid
		return
	}

	capabilities := GetProviderCapabilities(baseProvider)

	// Silently set unsupported operations to false instead of returning an error
	if allowedRequests.TextCompletion && !capabilities.TextCompletion {
		allowedRequests.TextCompletion = false
	}
	if allowedRequests.ChatCompletion && !capabilities.ChatCompletion {
		allowedRequests.ChatCompletion = false
	}
	if allowedRequests.ChatCompletionStream && !capabilities.ChatCompletionStream {
		allowedRequests.ChatCompletionStream = false
	}
	if allowedRequests.Embedding && !capabilities.Embedding {
		allowedRequests.Embedding = false
	}
	if allowedRequests.Speech && !capabilities.Speech {
		allowedRequests.Speech = false
	}
	if allowedRequests.SpeechStream && !capabilities.SpeechStream {
		allowedRequests.SpeechStream = false
	}
	if allowedRequests.Transcription && !capabilities.Transcription {
		allowedRequests.Transcription = false
	}
	if allowedRequests.TranscriptionStream && !capabilities.TranscriptionStream {
		allowedRequests.TranscriptionStream = false
	}
}

type CustomProviderConfig struct {
	CustomProviderKey string           `json:"-"`                  // Custom provider key, internally set by Bifrost
	BaseProviderType  ModelProvider    `json:"base_provider_type"` // Base provider type
	AllowedRequests   *AllowedRequests `json:"allowed_requests,omitempty"`
}

// IsOperationAllowed checks if a specific operation is allowed for this custom provider
func (cpc *CustomProviderConfig) IsOperationAllowed(operation Operation) bool {
	if cpc == nil {
		return true // Default to allowed if no custom provider config
	}
	if cpc.AllowedRequests == nil {
		// Use provider capabilities as default when no explicit restrictions are set
		defaultAllowedRequests := GetDefaultAllowedRequestsForProvider(cpc.BaseProviderType)
		return defaultAllowedRequests.IsOperationAllowed(operation)
	}
	return cpc.AllowedRequests.IsOperationAllowed(operation)
}

// ProviderConfig represents the complete configuration for a provider.
// An array of ProviderConfig needs to be provided in GetConfigForProvider
// in your account interface implementation.
type ProviderConfig struct {
	NetworkConfig            NetworkConfig            `json:"network_config"`              // Network configuration
	ConcurrencyAndBufferSize ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"` // Concurrency settings
	// Logger instance, can be provided by the user or bifrost default logger is used if not provided
	Logger               Logger                `json:"-"`
	ProxyConfig          *ProxyConfig          `json:"proxy_config,omitempty"` // Proxy configuration
	SendBackRawResponse  bool                  `json:"send_back_raw_response"` // Send raw response back in the bifrost response (default: false)
	CustomProviderConfig *CustomProviderConfig `json:"custom_provider_config,omitempty"`
}

type Operation string

const (
	OperationTextCompletion       Operation = "text_completion"
	OperationChatCompletion       Operation = "chat_completion"
	OperationChatCompletionStream Operation = "chat_completion_stream"
	OperationEmbedding            Operation = "embedding"
	OperationSpeech               Operation = "speech"
	OperationSpeechStream         Operation = "speech_stream"
	OperationTranscription        Operation = "transcription"
	OperationTranscriptionStream  Operation = "transcription_stream"
)

func (config *ProviderConfig) CheckAndSetDefaults() {
	if config.ConcurrencyAndBufferSize.Concurrency == 0 {
		config.ConcurrencyAndBufferSize.Concurrency = DefaultConcurrency
	}

	if config.ConcurrencyAndBufferSize.BufferSize == 0 {
		config.ConcurrencyAndBufferSize.BufferSize = DefaultBufferSize
	}

	if config.NetworkConfig.DefaultRequestTimeoutInSeconds == 0 {
		config.NetworkConfig.DefaultRequestTimeoutInSeconds = DefaultRequestTimeoutInSeconds
	}

	if config.NetworkConfig.MaxRetries == 0 {
		config.NetworkConfig.MaxRetries = DefaultMaxRetries
	}

	if config.NetworkConfig.RetryBackoffInitial == 0 {
		config.NetworkConfig.RetryBackoffInitial = DefaultRetryBackoffInitial
	}

	if config.NetworkConfig.RetryBackoffMax == 0 {
		config.NetworkConfig.RetryBackoffMax = DefaultRetryBackoffMax
	}

	// Create a defensive copy of ExtraHeaders to prevent data races
	if config.NetworkConfig.ExtraHeaders != nil {
		headersCopy := make(map[string]string, len(config.NetworkConfig.ExtraHeaders))
		maps.Copy(headersCopy, config.NetworkConfig.ExtraHeaders)
		config.NetworkConfig.ExtraHeaders = headersCopy
	}
}

type PostHookRunner func(ctx *context.Context, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError)

// Provider defines the interface for AI model providers.
type Provider interface {
	// GetProviderKey returns the provider's identifier
	GetProviderKey() ModelProvider
	// TextCompletion performs a text completion request
	TextCompletion(ctx context.Context, model string, key Key, text string, params *ModelParameters) (*BifrostResponse, *BifrostError)
	// ChatCompletion performs a chat completion request
	ChatCompletion(ctx context.Context, model string, key Key, messages []BifrostMessage, params *ModelParameters) (*BifrostResponse, *BifrostError)
	// ChatCompletionStream performs a chat completion stream request
	ChatCompletionStream(ctx context.Context, postHookRunner PostHookRunner, model string, key Key, messages []BifrostMessage, params *ModelParameters) (chan *BifrostStream, *BifrostError)
	// Embedding performs an embedding request
	Embedding(ctx context.Context, model string, key Key, input *EmbeddingInput, params *ModelParameters) (*BifrostResponse, *BifrostError)
	// Speech performs a text to speech request
	Speech(ctx context.Context, model string, key Key, input *SpeechInput, params *ModelParameters) (*BifrostResponse, *BifrostError)
	// SpeechStream performs a text to speech stream request
	SpeechStream(ctx context.Context, postHookRunner PostHookRunner, model string, key Key, input *SpeechInput, params *ModelParameters) (chan *BifrostStream, *BifrostError)
	// Transcription performs a transcription request
	Transcription(ctx context.Context, model string, key Key, input *TranscriptionInput, params *ModelParameters) (*BifrostResponse, *BifrostError)
	// TranscriptionStream performs a transcription stream request
	TranscriptionStream(ctx context.Context, postHookRunner PostHookRunner, model string, key Key, input *TranscriptionInput, params *ModelParameters) (chan *BifrostStream, *BifrostError)
}
