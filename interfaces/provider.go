package interfaces

// TODO third party providers

type NetworkConfig struct {
	DefaultRequestTimeoutInSeconds int `json:"default_request_timeout_in_seconds"`
}

type MetaConfig struct {
	SecretAccessKey   *string           `json:"secret_access_key,omitempty"`
	Region            *string           `json:"region,omitempty"`
	SessionToken      *string           `json:"session_token,omitempty"`
	ARN               *string           `json:"arn,omitempty"`
	InferenceProfiles map[string]string `json:"inference_profiles,omitempty"`
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

type ProviderConfig struct {
	NetworkConfig            NetworkConfig            `json:"network_config"`
	MetaConfig               *MetaConfig              `json:"meta_config,omitempty"`
	ConcurrencyAndBufferSize ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`
	Logger                   Logger                   `json:"logger"`
}

// Provider defines the interface for AI model providers
type Provider interface {
	GetProviderKey() SupportedModelProvider
	TextCompletion(model, key, text string, params *ModelParameters) (*BifrostResponse, *BifrostError)
	ChatCompletion(model, key string, messages []Message, params *ModelParameters) (*BifrostResponse, *BifrostError)
}
