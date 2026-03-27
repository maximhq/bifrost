// Package schemas defines the core schemas and types used by the Bifrost system.
package schemas

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type KeyStatusType string

const (
	KeyStatusSuccess          KeyStatusType = "success"
	KeyStatusListModelsFailed KeyStatusType = "list_models_failed"
)

// WhiteList is a list of values that are allowed to be used.
// Semantics:
//   - "*" (alone) means all values are allowed.
//   - Empty list means nothing is allowed.
//   - Non-empty list (without "*") means only the listed values are allowed.
//
// This type is used generically for any field that needs whitelist behavior
// (e.g., allowed models, allowed tools).
type WhiteList []string

// Contains reports whether value is in the whitelist.
// Returns true if value is in the list.
func (wl WhiteList) Contains(value string) bool {
	return slices.ContainsFunc(wl, func(s string) bool {
		return strings.EqualFold(s, value)
	})
}

// IsAllowed reports whether value is in the whitelist.
// Returns true if value is in the list.
func (wl WhiteList) IsAllowed(value string) bool {
	return wl.IsUnrestricted() || wl.Contains(value)
}

// IsEmpty reports whether the whitelist has no entries.
func (wl WhiteList) IsEmpty() bool {
	return len(wl) == 0
}

// IsUnrestricted reports whether the whitelist contains only "*",
// meaning all values are allowed.
func (wl WhiteList) IsUnrestricted() bool {
	return len(wl) == 1 && wl[0] == "*"
}

// IsRestricted reports whether the whitelist contains entries other than "*",
// meaning only the listed values are allowed.
func (wl WhiteList) IsRestricted() bool {
	return !wl.IsUnrestricted()
}

// Validate checks that the whitelist is well-formed.
// Returns an error if "*" is present alongside other values, or if there are duplicate entries.
func (wl WhiteList) Validate() error {
	if wl.Contains("*") && len(wl) > 1 {
		return fmt.Errorf("wildcard '*' cannot be used with other values in the whitelist")
	}
	seen := make(map[string]struct{}, len(wl))
	for _, v := range wl {
		normalized := strings.ToLower(v)
		if _, ok := seen[normalized]; ok {
			return fmt.Errorf("duplicate value '%s' in whitelist", v)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

// BlackList is a list of values that are denied.
// Semantics:
//   - "*" (alone) means all values are blocked.
//   - Empty list means nothing is blocked.
//   - Non-empty list (without "*") means only the listed values are blocked.
type BlackList []string

func (bl BlackList) Contains(value string) bool {
	return slices.ContainsFunc(bl, func(s string) bool {
		return strings.EqualFold(s, value)
	})
}

// IsBlocked reports whether value is blocked.
func (bl BlackList) IsBlocked(value string) bool {
	return bl.IsBlockAll() || bl.Contains(value)
}

// IsEmpty reports whether the blacklist has no entries (nothing is blocked).
func (bl BlackList) IsEmpty() bool {
	return len(bl) == 0
}

// IsBlockAll reports whether the blacklist contains "*", meaning all values are blocked.
func (bl BlackList) IsBlockAll() bool {
	return len(bl) == 1 && bl[0] == "*"
}

// Validate checks that the blacklist is well-formed.
func (bl BlackList) Validate() error {
	if bl.Contains("*") && len(bl) > 1 {
		return fmt.Errorf("wildcard '*' cannot be used with other values in the blacklist")
	}
	seen := make(map[string]struct{}, len(bl))
	for _, v := range bl {
		normalized := strings.ToLower(v)
		if _, ok := seen[normalized]; ok {
			return fmt.Errorf("duplicate value '%s' in blacklist", v)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

// Key represents an API key and its associated configuration for a provider.
// It contains the key value, supported models, and a weight for load balancing.
type Key struct {
	ID                 string              `json:"id"`                             // The unique identifier for the key (used by bifrost to identify the key)
	Name               string              `json:"name"`                           // The name of the key (used by users to identify the key, not used by bifrost)
	Value              EnvVar              `json:"value"`                          // The actual API key value
	Models             WhiteList           `json:"models"`                         // List of models this key can access
	BlacklistedModels  BlackList           `json:"blacklisted_models"`             // List of models this key cannot access
	Weight             float64             `json:"weight"`                         // Weight for load balancing between multiple keys
	Aliases            KeyAliases          `json:"aliases,omitempty"`              // Mapping of model identifiers to inference profiles
	AzureKeyConfig     *AzureKeyConfig     `json:"azure_key_config,omitempty"`     // Azure-specific key configuration
	VertexKeyConfig    *VertexKeyConfig    `json:"vertex_key_config,omitempty"`    // Vertex-specific key configuration
	BedrockKeyConfig   *BedrockKeyConfig   `json:"bedrock_key_config,omitempty"`   // AWS Bedrock-specific key configuration
	VLLMKeyConfig      *VLLMKeyConfig      `json:"vllm_key_config,omitempty"`      // vLLM-specific key configuration
	ReplicateKeyConfig *ReplicateKeyConfig `json:"replicate_key_config,omitempty"` // Replicate-specific key configuration
	Enabled            *bool               `json:"enabled,omitempty"`              // Whether the key is active (default:true)
	UseForBatchAPI     *bool               `json:"use_for_batch_api,omitempty"`    // Whether this key can be used for batch API operations (default:false for new keys, migrated keys default to true)
	ConfigHash         string              `json:"config_hash,omitempty"`          // Hash of config.json version, used for change detection
	Status             KeyStatusType       `json:"status,omitempty"`               // Status of key
	Description        string              `json:"description,omitempty"`          // Description of key
}

type KeyAliases map[string]string

func (ka KeyAliases) Resolve(model string) string {
	if ka == nil {
		return model
	}
	if alias, ok := ka[model]; ok {
		return alias
	}
	return model
}

type AzureAuthType string

const (
	AzureAuthTypeClientSecret    AzureAuthType = "client_secret"
	AzureAuthTypeManagedIdentity AzureAuthType = "managed_identity"
)

// AzureKeyConfig represents the Azure-specific configuration.
// It contains Azure-specific settings required for service access and deployment management.
type AzureKeyConfig struct {
	Endpoint   EnvVar  `json:"endpoint"`              // Azure service endpoint URL
	APIVersion *EnvVar `json:"api_version,omitempty"` // Azure API version to use; defaults to "2024-10-21"

	ClientID     *EnvVar  `json:"client_id,omitempty"`     // Azure client ID for authentication
	ClientSecret *EnvVar  `json:"client_secret,omitempty"` // Azure client secret for authentication
	TenantID     *EnvVar  `json:"tenant_id,omitempty"`     // Azure tenant ID for authentication
	Scopes       []string `json:"scopes,omitempty"`
}

// VertexKeyConfig represents the Vertex-specific configuration.
// It contains Vertex-specific settings required for authentication and service access.
type VertexKeyConfig struct {
	ProjectID       EnvVar `json:"project_id"`
	ProjectNumber   EnvVar `json:"project_number"`
	Region          EnvVar `json:"region"`
	AuthCredentials EnvVar `json:"auth_credentials"`
}

// NOTE: To use Vertex IAM role authentication, set AuthCredentials to empty string.

// S3BucketConfig represents a single S3 bucket configuration for batch operations.
type S3BucketConfig struct {
	BucketName string `json:"bucket_name"`          // S3 bucket name
	Prefix     string `json:"prefix,omitempty"`     // S3 key prefix for batch files
	IsDefault  bool   `json:"is_default,omitempty"` // Whether this is the default bucket for batch operations
}

// BatchS3Config holds S3 bucket configurations for Bedrock batch operations.
// Supports multiple buckets to allow flexible batch job routing.
type BatchS3Config struct {
	Buckets []S3BucketConfig `json:"buckets,omitempty"` // List of S3 bucket configurations
}

// BedrockKeyConfig represents the AWS Bedrock-specific configuration.
// It contains AWS-specific settings required for authentication and service access.
type BedrockKeyConfig struct {
	AccessKey    EnvVar  `json:"access_key,omitempty"`    // AWS access key for authentication
	SecretKey    EnvVar  `json:"secret_key,omitempty"`    // AWS secret access key for authentication
	SessionToken *EnvVar `json:"session_token,omitempty"` // AWS session token for temporary credentials
	Region       *EnvVar `json:"region,omitempty"`        // AWS region for service access
	ARN          *EnvVar `json:"arn,omitempty"`           // Amazon Resource Name for resource identification
	// IAM role for STS AssumeRole
	RoleARN         *EnvVar `json:"role_arn,omitempty"`
	ExternalID      *EnvVar `json:"external_id,omitempty"`
	RoleSessionName *EnvVar `json:"session_name,omitempty"`

	BatchS3Config *BatchS3Config `json:"batch_s3_config,omitempty"` // S3 bucket configuration for batch operations
}

// NOTE: To use Bedrock IAM role authentication, set both AccessKey and SecretKey to empty strings.
// To use Bedrock API Key authentication, set Value in Key struct instead.

// VLLMKeyConfig represents the vLLM-specific key configuration.
// It allows each key to target a different vLLM server URL and model name,
// enabling per-key routing and round-robin load balancing across multiple vLLM instances.
type VLLMKeyConfig struct {
	URL       EnvVar `json:"url"`        // VLLM server base URL (required, supports env. prefix)
	ModelName string `json:"model_name"` // Exact model name served on this VLLM instance (used for key selection)
}

// ReplicateKeyConfig represents the Replicate-specific key configuration.
// It contains Replicate-specific settings required for authentication and service access.
type ReplicateKeyConfig struct {
	UseDeploymentsEndpoint bool `json:"use_deployments_endpoint"` // Whether to use the deployments endpoint instead of the models endpoint
}

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
	GetKeysForProvider(ctx context.Context, providerKey ModelProvider) ([]Key, error)

	// GetConfigForProvider returns the configuration for a specific provider.
	// This includes network settings, authentication details, and other provider-specific
	// configurations.
	GetConfigForProvider(providerKey ModelProvider) (*ProviderConfig, error)
}
