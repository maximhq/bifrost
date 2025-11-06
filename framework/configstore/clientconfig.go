package configstore

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

type EnvKeyType string

const (
	EnvKeyTypeAPIKey        EnvKeyType = "api_key"
	EnvKeyTypeAzureConfig   EnvKeyType = "azure_config"
	EnvKeyTypeVertexConfig  EnvKeyType = "vertex_config"
	EnvKeyTypeBedrockConfig EnvKeyType = "bedrock_config"
	EnvKeyTypeConnection    EnvKeyType = "connection_string"
	EnvKeyTypeMCPHeader     EnvKeyType = "mcp_header"
)

// EnvKeyInfo stores information about a key sourced from environment
type EnvKeyInfo struct {
	EnvVar     string                // The environment variable name (without env. prefix)
	Provider   schemas.ModelProvider // The provider this key belongs to (empty for core/mcp configs)
	KeyType    EnvKeyType            // Type of key (e.g., "api_key", "azure_config", "vertex_config", "bedrock_config", "connection_string", "mcp_header")
	ConfigPath string                // Path in config where this env var is used
	KeyID      string                // The key ID this env var belongs to (empty for non-key configs like bedrock_config, connection_string)
}

// ClientConfig represents the core configuration for Bifrost HTTP transport and the Bifrost Client.
// It includes settings for excess request handling, Prometheus metrics, and initial pool size.
type ClientConfig struct {
	DropExcessRequests      bool     `json:"drop_excess_requests"`      // Drop excess requests if the provider queue is full
	InitialPoolSize         int      `json:"initial_pool_size"`         // The initial pool size for the bifrost client
	PrometheusLabels        []string `json:"prometheus_labels"`         // The labels to be used for prometheus metrics
	EnableLogging           bool     `json:"enable_logging"`            // Enable logging of requests and responses
	EnableGovernance        bool     `json:"enable_governance"`         // Enable governance on all requests
	EnforceGovernanceHeader bool     `json:"enforce_governance_header"` // Enforce governance on all requests
	AllowDirectKeys         bool     `json:"allow_direct_keys"`         // Allow direct keys to be used for requests
	AllowedOrigins          []string `json:"allowed_origins,omitempty"` // Additional allowed origins for CORS and WebSocket (localhost is always allowed)
	MaxRequestBodySizeMB    int      `json:"max_request_body_size_mb"`  // The maximum request body size in MB
	EnableLiteLLMFallbacks  bool     `json:"enable_litellm_fallbacks"`  // Enable litellm-specific fallbacks for text completion for Groq
}

// ProviderConfig represents the configuration for a specific AI model provider.
// It includes API keys, network settings, and concurrency settings.
type ProviderConfig struct {
	Keys                     []schemas.Key                     `json:"keys"`                                  // API keys for the provider with UUIDs
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config,omitempty"`                // Proxy configuration
	SendBackRawResponse      bool                              `json:"send_back_raw_response"`                // Include raw response in BifrostResponse
	CustomProviderConfig     *schemas.CustomProviderConfig     `json:"custom_provider_config,omitempty"`      // Custom provider configuration
}

// AuthConfig represents configured auth config for Bifrost dashboard
type AuthConfig struct {
	AdminUserName string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
	IsEnabled     bool   `json:"is_enabled"`
}

// ConfigMap maps provider names to their configurations.
type ConfigMap map[schemas.ModelProvider]ProviderConfig

type GovernanceConfig struct {
	VirtualKeys []tables.TableVirtualKey `json:"virtual_keys"`
	Teams       []tables.TableTeam       `json:"teams"`
	Customers   []tables.TableCustomer   `json:"customers"`
	Budgets     []tables.TableBudget     `json:"budgets"`
	RateLimits  []tables.TableRateLimit  `json:"rate_limits"`
	AuthConfig  *AuthConfig              `json:"auth_config,omitempty"`
}
