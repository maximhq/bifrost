package sapaicore

import (
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// SAPAICoreProvider implements the Provider interface for SAP AI Core.
type SAPAICoreProvider struct {
	logger               schemas.Logger
	client               *fasthttp.Client
	networkConfig        schemas.NetworkConfig
	sendBackRawRequest   bool
	sendBackRawResponse  bool
	customProviderConfig *schemas.CustomProviderConfig
	tokenCache           *TokenCache
	deploymentCache      *DeploymentCache
	stopCleanup          chan struct{}
	shutdownOnce         sync.Once
}

// SAPAICoreAPIVersion is the default API version for SAP AI Core OpenAI-compatible endpoints
const SAPAICoreAPIVersion = "2024-12-01-preview"

// SAPAICoreBackendType represents the backend type for SAP AI Core deployments
type SAPAICoreBackendType string

const (
	SAPAICoreBackendOpenAI  SAPAICoreBackendType = "openai"
	SAPAICoreBackendBedrock SAPAICoreBackendType = "bedrock"
	SAPAICoreBackendVertex  SAPAICoreBackendType = "vertex"
)

// SAPAICoreDeploymentStatus represents the status of a SAP AI Core deployment
type SAPAICoreDeploymentStatus string

const (
	SAPAICoreDeploymentStatusRunning SAPAICoreDeploymentStatus = "RUNNING"
	SAPAICoreDeploymentStatusStopped SAPAICoreDeploymentStatus = "STOPPED"
	SAPAICoreDeploymentStatusPending SAPAICoreDeploymentStatus = "PENDING"
	SAPAICoreDeploymentStatusDead    SAPAICoreDeploymentStatus = "DEAD"
)

// SAPAICoreDeploymentResource represents a SAP AI Core deployment from the deployments API
type SAPAICoreDeploymentResource struct {
	ID      string                     `json:"id"`
	Status  SAPAICoreDeploymentStatus  `json:"status"`
	Details SAPAICoreDeploymentDetails `json:"details"`
}

// SAPAICoreDeploymentDetails contains details about a deployment
type SAPAICoreDeploymentDetails struct {
	Resources SAPAICoreDeploymentResourceDetails `json:"resources"`
}

// SAPAICoreDeploymentResourceDetails contains resource details
type SAPAICoreDeploymentResourceDetails struct {
	SAPAICoreBackendDetails SAPAICoreBackendDetails `json:"backendDetails"`
}

// SAPAICoreBackendDetails contains backend model information
type SAPAICoreBackendDetails struct {
	Model SAPAICoreBackendModel `json:"model"`
}

// SAPAICoreBackendModel contains model name and version
type SAPAICoreBackendModel struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// SAPAICoreDeploymentsResponse represents the response from the deployments API
type SAPAICoreDeploymentsResponse struct {
	Count     int                           `json:"count"`
	Resources []SAPAICoreDeploymentResource `json:"resources"`
}

// SAPAICoreTokenResponse represents the OAuth2 token response
type SAPAICoreTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// DefaultMaxTokens is the default max_tokens value used when the user does not provide one.
// This is necessary because the Anthropic Messages API (used by Bedrock InvokeModel) requires
// max_tokens on every request. The user can override via MaxCompletionTokens in request params.
// This follows the same pattern as anthropic.AnthropicDefaultMaxTokens and
// bedrock.DefaultCompletionMaxTokens.
const DefaultMaxTokens = 4096

// SAPAICoreCachedDeployment represents a cached deployment with its resolved ID
type SAPAICoreCachedDeployment struct {
	DeploymentID string
	ModelName    string
	Backend      SAPAICoreBackendType
}

// SAPAICoreModel represents a model available in SAP AI Core
type SAPAICoreModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	DeploymentID string `json:"deployment_id"`
}

// sapaicoreErrorResponse captures error responses from SAP AI Core across all backend formats.
// OpenAI format:   {"error": {"message": "...", "type": "...", "code": "...", "param": "..."}}
// Platform format: {"message": "...", "code": "...", "status": "..."}
// Bedrock format:  {"message": "...", "__type": "ValidationException"}
type sapaicoreErrorResponse struct {
	// OpenAI-envelope nested error
	Error *sapaicoreErrorField `json:"error,omitempty"`
	// Top-level fields from SAP AI Core platform and Bedrock errors
	Message string  `json:"message,omitempty"`
	Type    *string `json:"type,omitempty"`
	Code    *string `json:"code,omitempty"`
	Status  *string `json:"status,omitempty"`
	// Bedrock uses __type for error classification
	BedrockType *string `json:"__type,omitempty"`
	// Top-level event_id (OpenAI Responses API)
	EventID *string `json:"event_id,omitempty"`
}

// sapaicoreErrorField represents the nested error object in OpenAI-format responses.
type sapaicoreErrorField struct {
	Message string      `json:"message,omitempty"`
	Type    *string     `json:"type,omitempty"`
	Code    *string     `json:"code,omitempty"`
	Param   interface{} `json:"param,omitempty"`
	EventID *string     `json:"event_id,omitempty"`
}
