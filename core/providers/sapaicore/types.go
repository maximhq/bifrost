package sapaicore

// SAPAICoreAPIVersion is the default API version for SAP AI Core OpenAI-compatible endpoints
const SAPAICoreAPIVersion = "2024-12-01-preview"

// BackendType represents the backend type for SAP AI Core deployments
type BackendType string

const (
	BackendOpenAI  BackendType = "openai"
	BackendBedrock BackendType = "bedrock"
	BackendVertex  BackendType = "vertex"
)

// DeploymentStatus represents the status of a SAP AI Core deployment
type DeploymentStatus string

const (
	DeploymentStatusRunning DeploymentStatus = "RUNNING"
	DeploymentStatusStopped DeploymentStatus = "STOPPED"
	DeploymentStatusPending DeploymentStatus = "PENDING"
	DeploymentStatusDead    DeploymentStatus = "DEAD"
)

// DeploymentResource represents a SAP AI Core deployment from the deployments API
type DeploymentResource struct {
	ID      string            `json:"id"`
	Status  DeploymentStatus  `json:"status"`
	Details DeploymentDetails `json:"details"`
}

// DeploymentDetails contains details about a deployment
type DeploymentDetails struct {
	Resources DeploymentResourceDetails `json:"resources"`
}

// DeploymentResourceDetails contains resource details
type DeploymentResourceDetails struct {
	BackendDetails BackendDetails `json:"backendDetails"`
}

// BackendDetails contains backend model information
type BackendDetails struct {
	Model BackendModel `json:"model"`
}

// BackendModel contains model name and version
type BackendModel struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// DeploymentsResponse represents the response from the deployments API
type DeploymentsResponse struct {
	Count     int                  `json:"count"`
	Resources []DeploymentResource `json:"resources"`
}

// TokenResponse represents the OAuth2 token response
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// ModelConfig contains configuration for a specific model
type ModelConfig struct {
	MaxTokens     int
	ContextWindow int
}

// ModelConfigs contains configuration for known SAP AI Core models
var ModelConfigs = map[string]ModelConfig{
	// Anthropic models via Bedrock
	"anthropic--claude-4.5-sonnet": {MaxTokens: 64000, ContextWindow: 200000},
	"anthropic--claude-4-sonnet":   {MaxTokens: 64000, ContextWindow: 200000},
	"anthropic--claude-4-opus":     {MaxTokens: 64000, ContextWindow: 200000},
	"anthropic--claude-3.7-sonnet": {MaxTokens: 64000, ContextWindow: 200000},
	"anthropic--claude-3.5-sonnet": {MaxTokens: 8192, ContextWindow: 200000},
	"anthropic--claude-3-sonnet":   {MaxTokens: 4096, ContextWindow: 200000},
	"anthropic--claude-3-haiku":    {MaxTokens: 4096, ContextWindow: 200000},
	"anthropic--claude-3-opus":     {MaxTokens: 4096, ContextWindow: 200000},

	// Amazon models via Bedrock
	"amazon--nova-pro":   {MaxTokens: 5120, ContextWindow: 300000},
	"amazon--nova-lite":  {MaxTokens: 5120, ContextWindow: 300000},
	"amazon--nova-micro": {MaxTokens: 5120, ContextWindow: 128000},

	// Gemini models via Vertex
	"gemini-2.5-pro":   {MaxTokens: 65536, ContextWindow: 1048576},
	"gemini-2.5-flash": {MaxTokens: 65536, ContextWindow: 1048576},
	"gemini-2.0-flash": {MaxTokens: 8192, ContextWindow: 1048576},
	"gemini-1.5-pro":   {MaxTokens: 8192, ContextWindow: 2097152},
	"gemini-1.5-flash": {MaxTokens: 8192, ContextWindow: 1048576},

	// OpenAI models
	"gpt-4":        {MaxTokens: 4096, ContextWindow: 200000},
	"gpt-4o":       {MaxTokens: 16384, ContextWindow: 128000},
	"gpt-4o-mini":  {MaxTokens: 16384, ContextWindow: 128000},
	"gpt-4.1":      {MaxTokens: 32768, ContextWindow: 1047576},
	"gpt-4.1-mini": {MaxTokens: 32768, ContextWindow: 1047576},
	"gpt-4.1-nano": {MaxTokens: 32768, ContextWindow: 1047576},
	"gpt-5":        {MaxTokens: 128000, ContextWindow: 272000},
	"gpt-5-nano":   {MaxTokens: 128000, ContextWindow: 272000},
	"gpt-5-mini":   {MaxTokens: 128000, ContextWindow: 272000},

	// Reasoning models
	"o1":      {MaxTokens: 100000, ContextWindow: 200000},
	"o3":      {MaxTokens: 100000, ContextWindow: 200000},
	"o3-mini": {MaxTokens: 100000, ContextWindow: 200000},
	"o4-mini": {MaxTokens: 100000, ContextWindow: 200000},
}

// GetModelConfig returns the configuration for a model, with fallback defaults
func GetModelConfig(modelName string) ModelConfig {
	if config, ok := ModelConfigs[modelName]; ok {
		return config
	}
	// Default fallback
	return ModelConfig{MaxTokens: 8192, ContextWindow: 200000}
}

// CachedDeployment represents a cached deployment with its resolved ID
type CachedDeployment struct {
	DeploymentID string
	ModelName    string
	Backend      BackendType
}

// SAPAICoreModel represents a model available in SAP AI Core
type SAPAICoreModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	DeploymentID    string `json:"deployment_id"`
	ContextLength   int    `json:"context_length,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}
