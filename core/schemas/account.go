// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import "context"

// Key represents an API key and its associated configuration for a provider.
// It contains the key value, supported models, and a weight for load balancing.
type Key struct {
	ID               string            `json:"id"`                           // The unique identifier for the key (not used by bifrost, but can be used by users to identify the key)
	Value            string            `json:"value"`                        // The actual API key value
	Models           []string          `json:"models"`                       // List of models this key can access
	Weight           float64           `json:"weight"`                       // Weight for load balancing between multiple keys
	OpenAIKeyConfig  *OpenAIKeyConfig  `json:"openai_key_config,omitempty"`  // OpenAI-specific key configuration
	AzureKeyConfig   *AzureKeyConfig   `json:"azure_key_config,omitempty"`   // Azure-specific key configuration
	VertexKeyConfig  *VertexKeyConfig  `json:"vertex_key_config,omitempty"`  // Vertex-specific key configuration
	BedrockKeyConfig *BedrockKeyConfig `json:"bedrock_key_config,omitempty"` // AWS Bedrock-specific key configuration
}

// OpenAIKeyConfig represents the OpenAI-specific configuration.
// It contains OpenAI-specific settings required for which endpoint to use. (chat completion or responses api)
type OpenAIKeyConfig struct {
	UseResponsesAPI bool `json:"use_responses_api,omitempty"`
}

// AzureKeyConfig represents the Azure-specific configuration.
// It contains Azure-specific settings required for service access and deployment management.
type AzureKeyConfig struct {
	Endpoint    string            `json:"endpoint"`              // Azure service endpoint URL
	Deployments map[string]string `json:"deployments,omitempty"` // Mapping of model names to deployment names
	APIVersion  *string           `json:"api_version,omitempty"` // Azure API version to use; defaults to "2024-08-01-preview"
}

// VertexKeyConfig represents the Vertex-specific configuration.
// It contains Vertex-specific settings required for authentication and service access.
type VertexKeyConfig struct {
	ProjectID       string `json:"project_id,omitempty"`
	Region          string `json:"region,omitempty"`
	AuthCredentials string `json:"auth_credentials,omitempty"`
}

// NOTE: To use Vertex IAM role authentication, set AuthCredentials to empty string.

// BedrockKeyConfig represents the AWS Bedrock-specific configuration.
// It contains AWS-specific settings required for authentication and service access.
type BedrockKeyConfig struct {
	AccessKey    string            `json:"access_key,omitempty"`    // AWS access key for authentication
	SecretKey    string            `json:"secret_key,omitempty"`    // AWS secret access key for authentication
	SessionToken *string           `json:"session_token,omitempty"` // AWS session token for temporary credentials
	Region       *string           `json:"region,omitempty"`        // AWS region for service access
	ARN          *string           `json:"arn,omitempty"`           // Amazon Resource Name for resource identification
	Deployments  map[string]string `json:"deployments,omitempty"`   // Mapping of model identifiers to inference profiles
}

// NOTE: To use Bedrock IAM role authentication, set both AccessKey and SecretKey to empty strings.
// To use Bedrock API Key authentication, set Value in Key struct instead.

// Account defines the interface for managing provider accounts and their configurations.
// It provides methods to access provider-specific settings, API keys, and configurations.
type Account interface {
	// GetConfiguredProviders returns a list of providers that are configured
	// in the account. This is used to determine which providers are available for use.
	GetConfiguredProviders() ([]ModelProvider, error)

	// GetKeysForProvider returns the API keys configured for a specific provider.
	// The keys include their values, supported models, and weights for load balancing.
	// The context can carry data from any source that sets values before the Bifrost request,
	// including but not limited to plugin pre-hooks, application logic, or any in app middleware sharing the context.
	// This enables dynamic key selection based on any context values present during the request.
	GetKeysForProvider(ctx *context.Context, providerKey ModelProvider) ([]Key, error)

	// GetConfigForProvider returns the configuration for a specific provider.
	// This includes network settings, authentication details, and other provider-specific
	// configurations.
	GetConfigForProvider(providerKey ModelProvider) (*ProviderConfig, error)
}
