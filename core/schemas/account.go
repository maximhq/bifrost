// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

// Key represents an API key and its associated configuration for a provider.
// It contains the key value, supported models, and a weight for load balancing.
type Key struct {
	ID              string           `json:"id"`                          // The unique identifier for the key (not used by bifrost, but can be used by users to identify the key)
	Value           string           `json:"value"`                       // The actual API key value
	Models          []string         `json:"models"`                      // List of models this key can access
	Weight          float64          `json:"weight"`                      // Weight for load balancing between multiple keys
	AzureKeyConfig  *AzureKeyConfig  `json:"azure_key_config,omitempty"`  // Azure-specific key configuration
	VertexKeyConfig *VertexKeyConfig `json:"vertex_key_config,omitempty"` // Vertex-specific key configuration
}

// AzureKeyConfig represents the Azure-specific configuration.
// It contains Azure-specific settings required for service access and deployment management.
type AzureKeyConfig struct {
	Endpoint    string            `json:"endpoint"`              // Azure service endpoint URL
	Deployments map[string]string `json:"deployments,omitempty"` // Mapping of model names to deployment names
	APIVersion  *string           `json:"api_version,omitempty"` // Azure API version to use; defaults to "2024-02-01"
}

// VertexKeyConfig represents the Vertex-specific configuration.
// It contains Vertex-specific settings required for authentication and service access.
type VertexKeyConfig struct {
	ProjectID       string `json:"project_id,omitempty"`
	Region          string `json:"region,omitempty"`
	AuthCredentials string `json:"auth_credentials,omitempty"`
}

// Account defines the interface for managing provider accounts and their configurations.
// It provides methods to access provider-specific settings, API keys, and configurations.
type Account interface {
	// GetConfiguredProviders returns a list of providers that are configured
	// in the account. This is used to determine which providers are available for use.
	GetConfiguredProviders() ([]ModelProvider, error)

	// GetKeysForProvider returns the API keys configured for a specific provider.
	// The keys include their values, supported models, and weights for load balancing.
	GetKeysForProvider(providerKey ModelProvider) ([]Key, error)

	// GetConfigForProvider returns the configuration for a specific provider.
	// This includes network settings, authentication details, and other provider-specific
	// configurations.
	GetConfigForProvider(providerKey ModelProvider) (*ProviderConfig, error)
}
