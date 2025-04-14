// Package interfaces defines the core interfaces and types used by the Bifrost system.
package interfaces

import "time"

// Pre-defined errors for provider operations
var (
	ErrProviderRequest           = "failed to make HTTP request to provider API"
	ErrProviderResponseUnmarshal = "failed to unmarshal response from provider API"
	ErrProviderJSONMarshaling    = "failed to marshal request body to JSON"
	ErrProviderDecodeStructured  = "failed to decode provider's structured response"
	ErrProviderDecodeRaw         = "failed to decode provider's raw response"
	ErrProviderDecompress        = "failed to decompress provider's response"
)

// NetworkConfig represents the network configuration for provider connections.
type NetworkConfig struct {
	DefaultRequestTimeoutInSeconds int           `json:"default_request_timeout_in_seconds"` // Default timeout for requests
	MaxRetries                     int           `json:"max_retries"`                        // Maximum number of retries
	RetryBackoffInitial            time.Duration `json:"retry_backoff_initial"`              // Initial backoff duration
	RetryBackoffMax                time.Duration `json:"retry_backoff_max"`                  // Maximum backoff duration
}

// MetaConfig defines the interface for provider-specific configuration.
// Check /meta folder for implemented provider-specific meta configurations.
type MetaConfig interface {
	// GetSecretAccessKey returns the secret access key for authentication
	GetSecretAccessKey() *string
	// GetRegion returns the region for the provider
	GetRegion() *string
	// GetSessionToken returns the session token for authentication
	GetSessionToken() *string
	// GetARN returns the Amazon Resource Name (ARN)
	GetARN() *string
	// GetInferenceProfiles returns the inference profiles
	GetInferenceProfiles() map[string]string
	// GetEndpoint returns the provider endpoint
	GetEndpoint() *string
	// GetDeployments returns the deployment configurations
	GetDeployments() map[string]string
	// GetAPIVersion returns the API version
	GetAPIVersion() *string
}

// ConcurrencyAndBufferSize represents configuration for concurrent operations and buffer sizes.
type ConcurrencyAndBufferSize struct {
	Concurrency int `json:"concurrency"` // Number of concurrent operations. Also used as the initial pool size for the provider reponses.
	BufferSize  int `json:"buffer_size"` // Size of the buffer
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

// ProviderConfig represents the complete configuration for a provider.
// An array of ProviderConfig needs to provided in GetConfigForProvider
// in your account interface implementation.
type ProviderConfig struct {
	NetworkConfig            NetworkConfig            `json:"network_config"`              // Network configuration
	MetaConfig               MetaConfig               `json:"meta_config,omitempty"`       // Provider-specific configuration
	ConcurrencyAndBufferSize ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"` // Concurrency settings
	// Logger instance, can be provided by the user or bifrost default logger is used if not provided
	Logger      Logger       `json:"logger"`
	ProxyConfig *ProxyConfig `json:"proxy_config,omitempty"` // Proxy configuration
}

// Provider defines the interface for AI model providers.
type Provider interface {
	// GetProviderKey returns the provider's identifier
	GetProviderKey() SupportedModelProvider
	// TextCompletion performs a text completion request
	TextCompletion(model, key, text string, params *ModelParameters) (*BifrostResponse, *BifrostError)
	// ChatCompletion performs a chat completion request
	ChatCompletion(model, key string, messages []Message, params *ModelParameters) (*BifrostResponse, *BifrostError)
}
