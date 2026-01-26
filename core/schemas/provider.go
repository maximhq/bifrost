// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"encoding/json"
	"maps"
	"time"
)

const (
	DefaultMaxRetries              = 0
	DefaultRetryBackoffInitial     = 500 * time.Millisecond
	DefaultRetryBackoffMax         = 5 * time.Second
	DefaultRequestTimeoutInSeconds = 30
	DefaultBufferSize              = 5000
	DefaultConcurrency             = 1000
	DefaultStreamBufferSize        = 5000
)

// Pre-defined errors for provider operations
const (
	ErrProviderRequestTimedOut      = "request timed out (default is 30 seconds). You can increase it by setting the default_request_timeout_in_seconds in the network_config or in UI - Providers > Provider Name > Network Config."
	ErrRequestCancelled             = "request cancelled by caller"
	ErrRequestBodyConversion        = "failed to convert bifrost request to the expected provider request body"
	ErrProviderRequestMarshal       = "failed to marshal request body to JSON"
	ErrProviderCreateRequest        = "failed to create HTTP request to provider API"
	ErrProviderDoRequest            = "failed to execute HTTP request to provider API"
	ErrProviderNetworkError         = "network error occurred while connecting to provider API (DNS lookup, connection refused, etc.)"
	ErrProviderResponseDecode       = "failed to decode response body from provider API"
	ErrProviderResponseUnmarshal    = "failed to unmarshal response from provider API"
	ErrProviderResponseEmpty        = "empty response received from provider"
	ErrProviderResponseHTML         = "HTML response received from provider"
	ErrProviderRawRequestUnmarshal  = "failed to unmarshal raw request from provider API"
	ErrProviderRawResponseUnmarshal = "failed to unmarshal raw response from provider API"
	ErrProviderResponseDecompress   = "failed to decompress provider's response"
)

// NetworkConfig represents the network configuration for provider connections.
// ExtraHeaders is automatically copied during provider initialization to prevent data races.
//
// RetryBackoffInitial and RetryBackoffMax are stored internally as time.Duration (nanoseconds),
// but are serialized/deserialized to/from JSON as milliseconds (integers).
// This means:
//   - In JSON: values are represented as milliseconds (e.g., 1000 means 1000ms)
//   - In Go: values are time.Duration (e.g., 1000ms = 1000000000 nanoseconds)
//   - When unmarshaling from JSON: a value of 1000 is interpreted as 1000ms, not 1000ns
//   - When marshaling to JSON: a time.Duration is converted to milliseconds
type NetworkConfig struct {
	// BaseURL is supported for OpenAI, Anthropic, Cohere, Mistral, and Ollama providers (required for Ollama)
	BaseURL                        string            `json:"base_url,omitempty"`                 // Base URL for the provider (optional)
	ExtraHeaders                   map[string]string `json:"extra_headers,omitempty"`            // Additional headers to include in requests (optional)
	DefaultRequestTimeoutInSeconds int               `json:"default_request_timeout_in_seconds"` // Default timeout for requests
	MaxRetries                     int               `json:"max_retries"`                        // Maximum number of retries
	RetryBackoffInitial            time.Duration     `json:"retry_backoff_initial"`              // Initial backoff duration (stored as nanoseconds, JSON as milliseconds)
	RetryBackoffMax                time.Duration     `json:"retry_backoff_max"`                  // Maximum backoff duration (stored as nanoseconds, JSON as milliseconds)
}

// UnmarshalJSON customizes JSON unmarshaling for NetworkConfig.
// RetryBackoffInitial and RetryBackoffMax are interpreted as milliseconds in JSON,
// but stored as time.Duration (nanoseconds) internally.
func (nc *NetworkConfig) UnmarshalJSON(data []byte) error {
	// Use an alias type to avoid infinite recursion
	type NetworkConfigAlias struct {
		BaseURL                        string            `json:"base_url,omitempty"`
		ExtraHeaders                   map[string]string `json:"extra_headers,omitempty"`
		DefaultRequestTimeoutInSeconds int               `json:"default_request_timeout_in_seconds"`
		MaxRetries                     int               `json:"max_retries"`
		RetryBackoffInitial            int64             `json:"retry_backoff_initial"` // milliseconds in JSON
		RetryBackoffMax                int64             `json:"retry_backoff_max"`     // milliseconds in JSON
	}

	var alias NetworkConfigAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	// Copy all fields
	nc.BaseURL = alias.BaseURL
	nc.ExtraHeaders = alias.ExtraHeaders
	nc.DefaultRequestTimeoutInSeconds = alias.DefaultRequestTimeoutInSeconds
	nc.MaxRetries = alias.MaxRetries

	// Convert milliseconds to time.Duration (nanoseconds)
	// Only convert if value is greater than 0
	if alias.RetryBackoffInitial > 0 {
		nc.RetryBackoffInitial = time.Duration(alias.RetryBackoffInitial) * time.Millisecond
	}
	if alias.RetryBackoffMax > 0 {
		nc.RetryBackoffMax = time.Duration(alias.RetryBackoffMax) * time.Millisecond
	}

	return nil
}

// MarshalJSON customizes JSON marshaling for NetworkConfig.
// RetryBackoffInitial and RetryBackoffMax are converted from time.Duration (nanoseconds)
// to milliseconds (integers) in JSON.
func (nc NetworkConfig) MarshalJSON() ([]byte, error) {
	// Use an alias type to avoid infinite recursion
	type NetworkConfigAlias struct {
		BaseURL                        string            `json:"base_url,omitempty"`
		ExtraHeaders                   map[string]string `json:"extra_headers,omitempty"`
		DefaultRequestTimeoutInSeconds int               `json:"default_request_timeout_in_seconds"`
		MaxRetries                     int               `json:"max_retries"`
		RetryBackoffInitial            int64             `json:"retry_backoff_initial"` // milliseconds in JSON
		RetryBackoffMax                int64             `json:"retry_backoff_max"`     // milliseconds in JSON
	}

	alias := NetworkConfigAlias{
		BaseURL:                        nc.BaseURL,
		ExtraHeaders:                   nc.ExtraHeaders,
		DefaultRequestTimeoutInSeconds: nc.DefaultRequestTimeoutInSeconds,
		MaxRetries:                     nc.MaxRetries,
		// Convert time.Duration (nanoseconds) to milliseconds
		RetryBackoffInitial: int64(nc.RetryBackoffInitial / time.Millisecond),
		RetryBackoffMax:     int64(nc.RetryBackoffMax / time.Millisecond),
	}

	return json.Marshal(alias)
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
	// HTTPProxy indicates an HTTP proxy should be used
	HTTPProxy ProxyType = "http"
	// Socks5Proxy indicates a SOCKS5 proxy should be used
	Socks5Proxy ProxyType = "socks5"
	// EnvProxy indicates the proxy should be read from environment variables
	EnvProxy ProxyType = "environment"
)

// ProxyConfig holds the configuration for proxy settings.
type ProxyConfig struct {
	Type      ProxyType `json:"type"`        // Type of proxy to use
	URL       string    `json:"url"`         // URL of the proxy server
	Username  string    `json:"username"`    // Username for proxy authentication
	Password  string    `json:"password"`    // Password for proxy authentication
	CACertPEM string    `json:"ca_cert_pem"` // PEM-encoded CA certificate to trust for TLS connections through the proxy
}

// Redacted returns a redacted copy of the proxy configuration.
func (pc *ProxyConfig) Redacted() *ProxyConfig {
	// Create redacted config with same structure but redacted values
	redactedConfig := ProxyConfig{
		Type:     pc.Type,
		URL:      pc.URL,
		Username: pc.Username,
	}
	if pc.Password != "" {
		redactedConfig.Password = "********"
	}
	if pc.CACertPEM != "" {
		redactedConfig.CACertPEM = "********"
	}
	return &redactedConfig
}

// AllowedRequests controls which operations are permitted.
// A nil *AllowedRequests means "all operations allowed."
// A non-nil value only allows fields set to true; omitted or false fields are disallowed.
type AllowedRequests struct {
	ListModels            bool `json:"list_models"`
	TextCompletion        bool `json:"text_completion"`
	TextCompletionStream  bool `json:"text_completion_stream"`
	ChatCompletion        bool `json:"chat_completion"`
	ChatCompletionStream  bool `json:"chat_completion_stream"`
	Responses             bool `json:"responses"`
	ResponsesStream       bool `json:"responses_stream"`
	CountTokens           bool `json:"count_tokens"`
	Embedding             bool `json:"embedding"`
	Speech                bool `json:"speech"`
	SpeechStream          bool `json:"speech_stream"`
	Transcription         bool `json:"transcription"`
	TranscriptionStream   bool `json:"transcription_stream"`
	ImageGeneration       bool `json:"image_generation"`
	ImageGenerationStream bool `json:"image_generation_stream"`
	BatchCreate           bool `json:"batch_create"`
	BatchList             bool `json:"batch_list"`
	BatchRetrieve         bool `json:"batch_retrieve"`
	BatchCancel           bool `json:"batch_cancel"`
	BatchResults          bool `json:"batch_results"`
	FileUpload            bool `json:"file_upload"`
	FileList              bool `json:"file_list"`
	FileRetrieve          bool `json:"file_retrieve"`
	FileDelete            bool `json:"file_delete"`
	FileContent           bool `json:"file_content"`
	ContainerCreate       bool `json:"container_create"`
	ContainerList         bool `json:"container_list"`
	ContainerRetrieve     bool `json:"container_retrieve"`
	ContainerDelete       bool `json:"container_delete"`
	ContainerFileCreate   bool `json:"container_file_create"`
	ContainerFileList     bool `json:"container_file_list"`
	ContainerFileRetrieve bool `json:"container_file_retrieve"`
	ContainerFileContent  bool `json:"container_file_content"`
	ContainerFileDelete   bool `json:"container_file_delete"`
}

// IsOperationAllowed checks if a specific operation is allowed
func (ar *AllowedRequests) IsOperationAllowed(operation RequestType) bool {
	if ar == nil {
		return true // Default to allowed if no restrictions
	}

	switch operation {
	case ListModelsRequest:
		return ar.ListModels
	case TextCompletionRequest:
		return ar.TextCompletion
	case TextCompletionStreamRequest:
		return ar.TextCompletionStream
	case ChatCompletionRequest:
		return ar.ChatCompletion
	case ChatCompletionStreamRequest:
		return ar.ChatCompletionStream
	case ResponsesRequest:
		return ar.Responses
	case ResponsesStreamRequest:
		return ar.ResponsesStream
	case CountTokensRequest:
		return ar.CountTokens
	case EmbeddingRequest:
		return ar.Embedding
	case SpeechRequest:
		return ar.Speech
	case SpeechStreamRequest:
		return ar.SpeechStream
	case TranscriptionRequest:
		return ar.Transcription
	case TranscriptionStreamRequest:
		return ar.TranscriptionStream
	case ImageGenerationRequest:
		return ar.ImageGeneration
	case ImageGenerationStreamRequest:
		return ar.ImageGenerationStream
	case BatchCreateRequest:
		return ar.BatchCreate
	case BatchListRequest:
		return ar.BatchList
	case BatchRetrieveRequest:
		return ar.BatchRetrieve
	case BatchCancelRequest:
		return ar.BatchCancel
	case BatchResultsRequest:
		return ar.BatchResults
	case FileUploadRequest:
		return ar.FileUpload
	case FileListRequest:
		return ar.FileList
	case FileRetrieveRequest:
		return ar.FileRetrieve
	case FileDeleteRequest:
		return ar.FileDelete
	case FileContentRequest:
		return ar.FileContent
	case ContainerCreateRequest:
		return ar.ContainerCreate
	case ContainerListRequest:
		return ar.ContainerList
	case ContainerRetrieveRequest:
		return ar.ContainerRetrieve
	case ContainerDeleteRequest:
		return ar.ContainerDelete
	case ContainerFileCreateRequest:
		return ar.ContainerFileCreate
	case ContainerFileListRequest:
		return ar.ContainerFileList
	case ContainerFileRetrieveRequest:
		return ar.ContainerFileRetrieve
	case ContainerFileContentRequest:
		return ar.ContainerFileContent
	case ContainerFileDeleteRequest:
		return ar.ContainerFileDelete
	default:
		return false // Default to not allowed for unknown operations
	}
}

type CustomProviderConfig struct {
	CustomProviderKey    string                 `json:"-"`                                // Custom provider key, internally set by Bifrost
	IsKeyLess            bool                   `json:"is_key_less"`                      // Whether the custom provider requires a key (not allowed for Bedrock)
	BaseProviderType     ModelProvider          `json:"base_provider_type"`               // Base provider type
	AllowedRequests      *AllowedRequests       `json:"allowed_requests,omitempty"`       // Allowed requests for the custom provider
	RequestPathOverrides map[RequestType]string `json:"request_path_overrides,omitempty"` // Mapping of request type to its custom path which will override the default path of the provider (not allowed for Bedrock)
}

// IsOperationAllowed checks if a specific operation is allowed for this custom provider
func (cpc *CustomProviderConfig) IsOperationAllowed(operation RequestType) bool {
	if cpc == nil || cpc.AllowedRequests == nil {
		return true // Default to allowed if no restrictions
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
	SendBackRawRequest   bool                  `json:"send_back_raw_request"`  // Send raw request back in the bifrost response (default: false)
	SendBackRawResponse  bool                  `json:"send_back_raw_response"` // Send raw response back in the bifrost response (default: false)
	CustomProviderConfig *CustomProviderConfig `json:"custom_provider_config,omitempty"`
}

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

type PostHookRunner func(ctx *BifrostContext, result *BifrostResponse, err *BifrostError) (*BifrostResponse, *BifrostError)

// Provider defines the interface for AI model providers.
type Provider interface {
	// GetProviderKey returns the provider's identifier
	GetProviderKey() ModelProvider
	// ListModels performs a list models request
	ListModels(ctx *BifrostContext, keys []Key, request *BifrostListModelsRequest) (*BifrostListModelsResponse, *BifrostError)
	// TextCompletion performs a text completion request
	TextCompletion(ctx *BifrostContext, key Key, request *BifrostTextCompletionRequest) (*BifrostTextCompletionResponse, *BifrostError)
	// TextCompletionStream performs a text completion stream request
	TextCompletionStream(ctx *BifrostContext, postHookRunner PostHookRunner, key Key, request *BifrostTextCompletionRequest) (chan *BifrostStreamChunk, *BifrostError)
	// ChatCompletion performs a chat completion request
	ChatCompletion(ctx *BifrostContext, key Key, request *BifrostChatRequest) (*BifrostChatResponse, *BifrostError)
	// ChatCompletionStream performs a chat completion stream request
	ChatCompletionStream(ctx *BifrostContext, postHookRunner PostHookRunner, key Key, request *BifrostChatRequest) (chan *BifrostStreamChunk, *BifrostError)
	// Responses performs a completion request using the Responses API (uses chat completion request internally for non-openai providers)
	Responses(ctx *BifrostContext, key Key, request *BifrostResponsesRequest) (*BifrostResponsesResponse, *BifrostError)
	// ResponsesStream performs a completion request using the Responses API stream (uses chat completion stream request internally for non-openai providers)
	ResponsesStream(ctx *BifrostContext, postHookRunner PostHookRunner, key Key, request *BifrostResponsesRequest) (chan *BifrostStreamChunk, *BifrostError)
	// CountTokens performs a count tokens request
	CountTokens(ctx *BifrostContext, key Key, request *BifrostResponsesRequest) (*BifrostCountTokensResponse, *BifrostError)
	// Embedding performs an embedding request
	Embedding(ctx *BifrostContext, key Key, request *BifrostEmbeddingRequest) (*BifrostEmbeddingResponse, *BifrostError)
	// Speech performs a text to speech request
	Speech(ctx *BifrostContext, key Key, request *BifrostSpeechRequest) (*BifrostSpeechResponse, *BifrostError)
	// SpeechStream performs a text to speech stream request
	SpeechStream(ctx *BifrostContext, postHookRunner PostHookRunner, key Key, request *BifrostSpeechRequest) (chan *BifrostStreamChunk, *BifrostError)
	// Transcription performs a transcription request
	Transcription(ctx *BifrostContext, key Key, request *BifrostTranscriptionRequest) (*BifrostTranscriptionResponse, *BifrostError)
	// TranscriptionStream performs a transcription stream request
	TranscriptionStream(ctx *BifrostContext, postHookRunner PostHookRunner, key Key, request *BifrostTranscriptionRequest) (chan *BifrostStreamChunk, *BifrostError)
	// ImageGeneration performs an image generation request
	ImageGeneration(ctx *BifrostContext, key Key, request *BifrostImageGenerationRequest) (
		*BifrostImageGenerationResponse, *BifrostError)
	// ImageGenerationStream performs an image generation stream request
	ImageGenerationStream(ctx *BifrostContext, postHookRunner PostHookRunner, key Key,
		request *BifrostImageGenerationRequest) (chan *BifrostStreamChunk, *BifrostError)
	// BatchCreate creates a new batch job for asynchronous processing
	BatchCreate(ctx *BifrostContext, key Key, request *BifrostBatchCreateRequest) (*BifrostBatchCreateResponse, *BifrostError)
	// BatchList lists batch jobs
	BatchList(ctx *BifrostContext, keys []Key, request *BifrostBatchListRequest) (*BifrostBatchListResponse, *BifrostError)
	// BatchRetrieve retrieves a specific batch job
	BatchRetrieve(ctx *BifrostContext, keys []Key, request *BifrostBatchRetrieveRequest) (*BifrostBatchRetrieveResponse, *BifrostError)
	// BatchCancel cancels a batch job
	BatchCancel(ctx *BifrostContext, keys []Key, request *BifrostBatchCancelRequest) (*BifrostBatchCancelResponse, *BifrostError)
	// BatchResults retrieves results from a completed batch job
	BatchResults(ctx *BifrostContext, keys []Key, request *BifrostBatchResultsRequest) (*BifrostBatchResultsResponse, *BifrostError)
	// FileUpload uploads a file to the provider
	FileUpload(ctx *BifrostContext, key Key, request *BifrostFileUploadRequest) (*BifrostFileUploadResponse, *BifrostError)
	// FileList lists files from the provider
	FileList(ctx *BifrostContext, keys []Key, request *BifrostFileListRequest) (*BifrostFileListResponse, *BifrostError)
	// FileRetrieve retrieves file metadata from the provider
	FileRetrieve(ctx *BifrostContext, keys []Key, request *BifrostFileRetrieveRequest) (*BifrostFileRetrieveResponse, *BifrostError)
	// FileDelete deletes a file from the provider
	FileDelete(ctx *BifrostContext, keys []Key, request *BifrostFileDeleteRequest) (*BifrostFileDeleteResponse, *BifrostError)
	// FileContent downloads file content from the provider
	FileContent(ctx *BifrostContext, keys []Key, request *BifrostFileContentRequest) (*BifrostFileContentResponse, *BifrostError)
	// ContainerCreate creates a new container
	ContainerCreate(ctx *BifrostContext, key Key, request *BifrostContainerCreateRequest) (*BifrostContainerCreateResponse, *BifrostError)
	// ContainerList lists containers
	ContainerList(ctx *BifrostContext, keys []Key, request *BifrostContainerListRequest) (*BifrostContainerListResponse, *BifrostError)
	// ContainerRetrieve retrieves a specific container
	ContainerRetrieve(ctx *BifrostContext, keys []Key, request *BifrostContainerRetrieveRequest) (*BifrostContainerRetrieveResponse, *BifrostError)
	// ContainerDelete deletes a container
	ContainerDelete(ctx *BifrostContext, keys []Key, request *BifrostContainerDeleteRequest) (*BifrostContainerDeleteResponse, *BifrostError)
	// ContainerFileCreate creates a file in a container
	ContainerFileCreate(ctx *BifrostContext, key Key, request *BifrostContainerFileCreateRequest) (*BifrostContainerFileCreateResponse, *BifrostError)
	// ContainerFileList lists files in a container
	ContainerFileList(ctx *BifrostContext, keys []Key, request *BifrostContainerFileListRequest) (*BifrostContainerFileListResponse, *BifrostError)
	// ContainerFileRetrieve retrieves a file from a container
	ContainerFileRetrieve(ctx *BifrostContext, keys []Key, request *BifrostContainerFileRetrieveRequest) (*BifrostContainerFileRetrieveResponse, *BifrostError)
	// ContainerFileContent retrieves the content of a file from a container
	ContainerFileContent(ctx *BifrostContext, keys []Key, request *BifrostContainerFileContentRequest) (*BifrostContainerFileContentResponse, *BifrostError)
	// ContainerFileDelete deletes a file from a container
	ContainerFileDelete(ctx *BifrostContext, keys []Key, request *BifrostContainerFileDeleteRequest) (*BifrostContainerFileDeleteResponse, *BifrostError)
}
