package interfaces

import "time"

// Pre-defined errors
var (
	ErrProviderRequest           = "failed to make HTTP request to provider API"
	ErrProviderResponseUnmarshal = "failed to unmarshal response from provider API"
	ErrProviderJSONMarshaling    = "failed to marshal request body to JSON"
	ErrProviderDecodeStructured  = "failed to decode provider's structured response"
	ErrProviderDecodeRaw         = "failed to decode provider's raw response"
	ErrProviderDecompress        = "failed to decompress provider's response"
)

// TODO third party providers

type NetworkConfig struct {
	DefaultRequestTimeoutInSeconds int           `json:"default_request_timeout_in_seconds"`
	MaxRetries                     int           `json:"max_retries"`
	RetryBackoffInitial            time.Duration `json:"retry_backoff_initial"`
	RetryBackoffMax                time.Duration `json:"retry_backoff_max"`
}

type MetaConfig interface {
	GetSecretAccessKey() *string
	GetRegion() *string
	GetSessionToken() *string
	GetARN() *string
	GetInferenceProfiles() map[string]string
	GetEndpoint() *string
	GetDeployments() map[string]string
	GetAPIVersion() *string
}

type ConcurrencyAndBufferSize struct {
	Concurrency int `json:"concurrency"`
	BufferSize  int `json:"buffer_size"`
}

// ProxyType defines the type of proxy to use
type ProxyType string

const (
	NoProxy     ProxyType = "none"
	HttpProxy   ProxyType = "http"
	Socks5Proxy ProxyType = "socks5"
	EnvProxy    ProxyType = "environment"
)

// ProxyConfig holds proxy configuration
type ProxyConfig struct {
	Type     ProxyType `json:"type"`     // Type of proxy (none, http, socks5, environment)
	URL      string    `json:"url"`      // Proxy URL (for http and socks5)
	Username string    `json:"username"` // Optional username for proxy authentication
	Password string    `json:"password"` // Optional password for proxy authentication
}

type ProviderConfig struct {
	NetworkConfig            NetworkConfig            `json:"network_config"`
	MetaConfig               MetaConfig               `json:"meta_config,omitempty"`
	ConcurrencyAndBufferSize ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`
	Logger                   Logger                   `json:"logger"`
	ProxyConfig              *ProxyConfig             `json:"proxy_config,omitempty"`
}

// Provider defines the interface for AI model providers
type Provider interface {
	GetProviderKey() SupportedModelProvider
	TextCompletion(model, key, text string, params *ModelParameters) (*BifrostResponse, *BifrostError)
	ChatCompletion(model, key string, messages []Message, params *ModelParameters) (*BifrostResponse, *BifrostError)
}
