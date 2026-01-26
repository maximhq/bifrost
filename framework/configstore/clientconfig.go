package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
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
	DropExcessRequests      bool                             `json:"drop_excess_requests"`    // Drop excess requests if the provider queue is full
	InitialPoolSize         int                              `json:"initial_pool_size"`       // The initial pool size for the bifrost client
	PrometheusLabels        []string                         `json:"prometheus_labels"`       // The labels to be used for prometheus metrics
	EnableLogging           bool                             `json:"enable_logging"`          // Enable logging of requests and responses
	DisableContentLogging   bool                             `json:"disable_content_logging"` // Disable logging of content
	DisableDBPingsInHealth  bool                             `json:"disable_db_pings_in_health"`
	LogRetentionDays        int                              `json:"log_retention_days" validate:"min=1"` // Number of days to retain logs (minimum 1 day)
	EnableGovernance        bool                             `json:"enable_governance"`                   // Enable governance on all requests
	EnforceGovernanceHeader bool                             `json:"enforce_governance_header"`           // Enforce governance on all requests
	AllowDirectKeys         bool                             `json:"allow_direct_keys"`                   // Allow direct keys to be used for requests
	AllowedOrigins          []string                         `json:"allowed_origins,omitempty"`           // Additional allowed origins for CORS and WebSocket (localhost is always allowed)
	AllowedHeaders          []string                         `json:"allowed_headers,omitempty"`           // Additional allowed headers for CORS and WebSocket
	MaxRequestBodySizeMB    int                              `json:"max_request_body_size_mb"`            // The maximum request body size in MB
	EnableLiteLLMFallbacks  bool                             `json:"enable_litellm_fallbacks"`            // Enable litellm-specific fallbacks for text completion for Groq
	MCPAgentDepth           int                              `json:"mcp_agent_depth"`                     // The maximum depth for MCP agent mode tool execution
	MCPToolExecutionTimeout int                              `json:"mcp_tool_execution_timeout"`          // The timeout for individual tool execution in seconds
	MCPCodeModeBindingLevel string                           `json:"mcp_code_mode_binding_level"`         // Code mode binding level: "server" or "tool"
	MCPToolSyncInterval     int                              `json:"mcp_tool_sync_interval"`              // Global tool sync interval in minutes (default: 10, 0 = disabled)
	HeaderFilterConfig      *tables.GlobalHeaderFilterConfig `json:"header_filter_config,omitempty"`      // Global header filtering configuration for x-bf-eh-* headers
	ConfigHash              string                           `json:"-"`                                   // Config hash for reconciliation (not serialized)
}

// GenerateClientConfigHash generates a SHA256 hash of the client configuration.
// This is used to detect changes between config.json and database config.
func (c *ClientConfig) GenerateClientConfigHash() (string, error) {
	hash := sha256.New()

	// Hash boolean fields
	if c.DropExcessRequests {
		hash.Write([]byte("dropExcessRequests:true"))
	} else {
		hash.Write([]byte("dropExcessRequests:false"))
	}

	if c.EnableLogging {
		hash.Write([]byte("enableLogging:true"))
	} else {
		hash.Write([]byte("enableLogging:false"))
	}

	if c.DisableContentLogging {
		hash.Write([]byte("disableContentLogging:true"))
	} else {
		hash.Write([]byte("disableContentLogging:false"))
	}

	if c.DisableDBPingsInHealth {
		hash.Write([]byte("disableDBPingsInHealth:true"))
	} else {
		hash.Write([]byte("disableDBPingsInHealth:false"))
	}

	if c.EnableGovernance {
		hash.Write([]byte("enableGovernance:true"))
	} else {
		hash.Write([]byte("enableGovernance:false"))
	}

	if c.EnforceGovernanceHeader {
		hash.Write([]byte("enforceGovernanceHeader:true"))
	} else {
		hash.Write([]byte("enforceGovernanceHeader:false"))
	}

	if c.AllowDirectKeys {
		hash.Write([]byte("allowDirectKeys:true"))
	} else {
		hash.Write([]byte("allowDirectKeys:false"))
	}

	if c.EnableLiteLLMFallbacks {
		hash.Write([]byte("enableLiteLLMFallbacks:true"))
	} else {
		hash.Write([]byte("enableLiteLLMFallbacks:false"))
	}

	if c.MCPAgentDepth > 0 {
		hash.Write([]byte("mcpAgentDepth:" + strconv.Itoa(c.MCPAgentDepth)))
	} else {
		hash.Write([]byte("mcpAgentDepth:0"))
	}

	if c.MCPToolExecutionTimeout > 0 {
		hash.Write([]byte("mcpToolExecutionTimeout:" + strconv.Itoa(c.MCPToolExecutionTimeout)))
	} else {
		hash.Write([]byte("mcpToolExecutionTimeout:0"))
	}

	if c.MCPCodeModeBindingLevel != "" {
		hash.Write([]byte("mcpCodeModeBindingLevel:" + c.MCPCodeModeBindingLevel))
	} else {
		hash.Write([]byte("mcpCodeModeBindingLevel:server"))
	}

	if c.MCPToolSyncInterval > 0 {
		hash.Write([]byte("mcpToolSyncInterval:" + strconv.Itoa(c.MCPToolSyncInterval)))
	} else {
		hash.Write([]byte("mcpToolSyncInterval:0"))
	}

	// Hash integer fields
	data, err := sonic.Marshal(c.InitialPoolSize)
	if err != nil {
		return "", err
	}
	hash.Write(data)

	data, err = sonic.Marshal(c.LogRetentionDays)
	if err != nil {
		return "", err
	}
	hash.Write(data)

	data, err = sonic.Marshal(c.MaxRequestBodySizeMB)
	if err != nil {
		return "", err
	}
	hash.Write(data)

	// Hash PrometheusLabels (sorted for deterministic hashing)
	if len(c.PrometheusLabels) > 0 {
		sortedLabels := make([]string, len(c.PrometheusLabels))
		copy(sortedLabels, c.PrometheusLabels)
		sort.Strings(sortedLabels)
		data, err := sonic.Marshal(sortedLabels)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash AllowedOrigins (sorted for deterministic hashing)
	if len(c.AllowedOrigins) > 0 {
		sortedOrigins := make([]string, len(c.AllowedOrigins))
		copy(sortedOrigins, c.AllowedOrigins)
		sort.Strings(sortedOrigins)
		data, err := sonic.Marshal(sortedOrigins)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash AllowedHeaders (sorted for deterministic hashing)
	if len(c.AllowedHeaders) > 0 {
		sortedHeaders := make([]string, len(c.AllowedHeaders))
		copy(sortedHeaders, c.AllowedHeaders)
		sort.Strings(sortedHeaders)
		data, err := sonic.Marshal(sortedHeaders)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash HeaderFilterConfig
	if c.HeaderFilterConfig != nil {
		// Hash Allowlist (sorted for deterministic hashing)
		if len(c.HeaderFilterConfig.Allowlist) > 0 {
			sortedAllowlist := make([]string, len(c.HeaderFilterConfig.Allowlist))
			copy(sortedAllowlist, c.HeaderFilterConfig.Allowlist)
			sort.Strings(sortedAllowlist)
			data, err := sonic.Marshal(sortedAllowlist)
			if err != nil {
				return "", err
			}
			hash.Write([]byte("headerFilterConfig.allowlist:"))
			hash.Write(data)
		}
		// Hash Denylist (sorted for deterministic hashing)
		if len(c.HeaderFilterConfig.Denylist) > 0 {
			sortedDenylist := make([]string, len(c.HeaderFilterConfig.Denylist))
			copy(sortedDenylist, c.HeaderFilterConfig.Denylist)
			sort.Strings(sortedDenylist)
			data, err := sonic.Marshal(sortedDenylist)
			if err != nil {
				return "", err
			}
			hash.Write([]byte("headerFilterConfig.denylist:"))
			hash.Write(data)
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ProviderConfig represents the configuration for a specific AI model provider.
// It includes API keys, network settings, and concurrency settings.
type ProviderConfig struct {
	Keys                     []schemas.Key                     `json:"keys"`                                  // API keys for the provider with UUIDs
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
	ProxyConfig              *schemas.ProxyConfig              `json:"proxy_config,omitempty"`                // Proxy configuration
	SendBackRawRequest       bool                              `json:"send_back_raw_request"`                 // Include raw request in BifrostResponse
	SendBackRawResponse      bool                              `json:"send_back_raw_response"`                // Include raw response in BifrostResponse
	CustomProviderConfig     *schemas.CustomProviderConfig     `json:"custom_provider_config,omitempty"`      // Custom provider configuration
	ConfigHash               string                            `json:"config_hash,omitempty"`                 // Hash of config.json version, used for change detection
}

// Redacted returns a redacted copy of the provider configuration.
func (p *ProviderConfig) Redacted() *ProviderConfig {
	// Create redacted config with same structure but redacted values
	redactedConfig := ProviderConfig{
		NetworkConfig:            p.NetworkConfig,
		ConcurrencyAndBufferSize: p.ConcurrencyAndBufferSize,
		SendBackRawRequest:       p.SendBackRawRequest,
		SendBackRawResponse:      p.SendBackRawResponse,
		CustomProviderConfig:     p.CustomProviderConfig,
		ConfigHash:               p.ConfigHash,
	}

	if p.ProxyConfig != nil {
		redactedConfig.ProxyConfig = p.ProxyConfig.Redacted()
	}

	// Create redacted keys
	redactedConfig.Keys = make([]schemas.Key, len(p.Keys))
	for i, key := range p.Keys {
		models := key.Models
		if models == nil {
			models = []string{} // Ensure models is never nil in JSON response
		}
		redactedConfig.Keys[i] = schemas.Key{
			ID:         key.ID,
			Name:       key.Name,
			Models:     models,
			Weight:     key.Weight,
			ConfigHash: key.ConfigHash,
		}
		if key.Enabled != nil {
			enabled := *key.Enabled
			redactedConfig.Keys[i].Enabled = &enabled
		}
		redactedConfig.Keys[i].Value = *key.Value.Redacted()
		// Add back use for batch api
		if key.UseForBatchAPI != nil {
			redactedConfig.Keys[i].UseForBatchAPI = key.UseForBatchAPI
		} else {
			redactedConfig.Keys[i].UseForBatchAPI = bifrost.Ptr(false)
		}

		// Redact Azure key config if present
		if key.AzureKeyConfig != nil {
			azureConfig := &schemas.AzureKeyConfig{
				Deployments: key.AzureKeyConfig.Deployments,
			}
			azureConfig.Endpoint = *key.AzureKeyConfig.Endpoint.Redacted()
			azureConfig.APIVersion = key.AzureKeyConfig.APIVersion
			if key.AzureKeyConfig.ClientID != nil {
				azureConfig.ClientID = key.AzureKeyConfig.ClientID.Redacted()
			}
			if key.AzureKeyConfig.ClientSecret != nil {
				azureConfig.ClientSecret = key.AzureKeyConfig.ClientSecret.Redacted()
			}
			if key.AzureKeyConfig.TenantID != nil {
				azureConfig.TenantID = key.AzureKeyConfig.TenantID.Redacted()
			}
			redactedConfig.Keys[i].AzureKeyConfig = azureConfig
		}

		// Redact Vertex key config if present
		if key.VertexKeyConfig != nil {
			vertexConfig := &schemas.VertexKeyConfig{
				Deployments: key.VertexKeyConfig.Deployments,
			}
			vertexConfig.ProjectID = *key.VertexKeyConfig.ProjectID.Redacted()
			vertexConfig.ProjectNumber = *key.VertexKeyConfig.ProjectNumber.Redacted()
			vertexConfig.Region = *key.VertexKeyConfig.Region.Redacted()
			vertexConfig.AuthCredentials = *key.VertexKeyConfig.AuthCredentials.Redacted()
			redactedConfig.Keys[i].VertexKeyConfig = vertexConfig
		}

		// Redact Bedrock key config if present
		if key.BedrockKeyConfig != nil {
			bedrockConfig := &schemas.BedrockKeyConfig{
				Deployments: key.BedrockKeyConfig.Deployments,
			}
			bedrockConfig.AccessKey = *key.BedrockKeyConfig.AccessKey.Redacted()
			bedrockConfig.SecretKey = *key.BedrockKeyConfig.SecretKey.Redacted()
			if key.BedrockKeyConfig.SessionToken != nil {
				bedrockConfig.SessionToken = key.BedrockKeyConfig.SessionToken.Redacted()
			}
			if key.BedrockKeyConfig.Region != nil {
				bedrockConfig.Region = key.BedrockKeyConfig.Region.Redacted()
			}
			if key.BedrockKeyConfig.ARN != nil {
				bedrockConfig.ARN = key.BedrockKeyConfig.ARN.Redacted()
			}
			// Add back s3 config
			if key.BedrockKeyConfig.BatchS3Config != nil {
				bedrockConfig.BatchS3Config = key.BedrockKeyConfig.BatchS3Config
			}
			redactedConfig.Keys[i].BedrockKeyConfig = bedrockConfig
		}
	}
	return &redactedConfig
}

// GenerateConfigHash generates a SHA256 hash of the provider configuration.
// This is used to detect changes between config.json and database config.
// Keys are excluded as they are hashed separately.
func (p *ProviderConfig) GenerateConfigHash(providerName string) (string, error) {
	hash := sha256.New()

	// Hash provider name
	hash.Write([]byte(providerName))

	// Hash NetworkConfig
	if p.NetworkConfig != nil {
		data, err := sonic.Marshal(p.NetworkConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash ConcurrencyAndBufferSize
	if p.ConcurrencyAndBufferSize != nil {
		data, err := sonic.Marshal(p.ConcurrencyAndBufferSize)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash ProxyConfig
	if p.ProxyConfig != nil {
		data, err := sonic.Marshal(p.ProxyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash CustomProviderConfig
	if p.CustomProviderConfig != nil {
		data, err := sonic.Marshal(p.CustomProviderConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash SendBackRawRequest
	if p.SendBackRawRequest {
		hash.Write([]byte("sendBackRawRequest"))
	}

	// Hash SendBackRawResponse
	if p.SendBackRawResponse {
		hash.Write([]byte("sendBackRawResponse"))
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateKeyHash generates a SHA256 hash for an individual key.
// This is used to detect changes to keys between config.json and database.
// Skips: ID (dynamic UUID), timestamps
func GenerateKeyHash(key schemas.Key) (string, error) {
	hash := sha256.New()
	// Hash Name
	hash.Write([]byte(key.Name))
	// Hash Value
	if key.Value.IsFromEnv() {
		hash.Write([]byte(key.Value.EnvVar))
	} else {
		hash.Write([]byte(key.Value.Val))
	}
	// Hash Models (key-level model restrictions)
	if len(key.Models) > 0 {
		sortedModels := make([]string, len(key.Models))
		copy(sortedModels, key.Models)
		sort.Strings(sortedModels)
		data, err := sonic.Marshal(sortedModels)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash Weight
	data, err := sonic.Marshal(key.Weight)
	if err != nil {
		return "", err
	}
	hash.Write(data)
	// Hash AzureKeyConfig
	if key.AzureKeyConfig != nil {
		data, err := sonic.Marshal(key.AzureKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash VertexKeyConfig
	if key.VertexKeyConfig != nil {
		data, err := sonic.Marshal(key.VertexKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash BedrockKeyConfig
	if key.BedrockKeyConfig != nil {
		data, err := sonic.Marshal(key.BedrockKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash Enabled (nil = false, only true produces different hash)
	if key.Enabled != nil && *key.Enabled {
		hash.Write([]byte("enabled:true"))
	}
	// Hash UseForBatchAPI (nil = default false for new keys)
	useForBatchAPI := false
	if key.UseForBatchAPI != nil {
		useForBatchAPI = *key.UseForBatchAPI
	}
	if useForBatchAPI {
		hash.Write([]byte("useForBatchAPI:true"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// VirtualKeyHashInput represents the fields used for virtual key hash generation.
// This struct is used to create a consistent hash from TableVirtualKey,
// excluding dynamic fields like ID, timestamps, and relationship objects.
type VirtualKeyHashInput struct {
	Name        string
	Description string
	IsActive    bool
	TeamID      *string
	CustomerID  *string
	BudgetID    *string
	RateLimitID *string
	// ProviderConfigs and MCPConfigs are hashed separately as they contain nested data
	ProviderConfigs []VirtualKeyProviderConfigHashInput
	MCPConfigs      []VirtualKeyMCPConfigHashInput
}

// VirtualKeyProviderConfigHashInput represents provider config fields for hashing
type VirtualKeyProviderConfigHashInput struct {
	Provider      string
	Weight        float64
	AllowedModels []string
	BudgetID      *string
	RateLimitID   *string
	KeyIDs        []string // Only key IDs, not full key objects
}

// VirtualKeyMCPConfigHashInput represents MCP config fields for hashing
type VirtualKeyMCPConfigHashInput struct {
	MCPClientID    uint
	ToolsToExecute []string
}

// GenerateVirtualKeyHash generates a SHA256 hash for a virtual key.
// This is used to detect changes to virtual keys between config.json and database.
// Skips: ID (primary key), CreatedAt, UpdatedAt, and relationship objects (Team, Customer, Budget, RateLimit)
func GenerateVirtualKeyHash(vk tables.TableVirtualKey) (string, error) {
	hash := sha256.New()
	// Hash Name
	hash.Write([]byte(vk.Name))
	// Hash Description
	hash.Write([]byte(vk.Description))
	// Hash Value
	hash.Write([]byte(vk.Value))
	// Hash IsActive
	if vk.IsActive {
		hash.Write([]byte("isActive:true"))
	} else {
		hash.Write([]byte("isActive:false"))
	}
	// Hash TeamID
	if vk.TeamID != nil {
		hash.Write([]byte("teamID:" + *vk.TeamID))
	}
	// Hash CustomerID
	if vk.CustomerID != nil {
		hash.Write([]byte("customerID:" + *vk.CustomerID))
	}
	// Hash BudgetID
	if vk.BudgetID != nil {
		hash.Write([]byte("budgetID:" + *vk.BudgetID))
	}
	// Hash RateLimitID
	if vk.RateLimitID != nil {
		hash.Write([]byte("rateLimitID:" + *vk.RateLimitID))
	}
	// Hash ProviderConfigs
	if len(vk.ProviderConfigs) > 0 {
		// Copy and sort provider configs for deterministic hashing
		sortedProviderConfigs := make([]tables.TableVirtualKeyProviderConfig, len(vk.ProviderConfigs))
		copy(sortedProviderConfigs, vk.ProviderConfigs)
		sort.Slice(sortedProviderConfigs, func(i, j int) bool {
			if sortedProviderConfigs[i].Provider != sortedProviderConfigs[j].Provider {
				return sortedProviderConfigs[i].Provider < sortedProviderConfigs[j].Provider
			}
			bi, bj := "", ""
			if sortedProviderConfigs[i].BudgetID != nil {
				bi = *sortedProviderConfigs[i].BudgetID
			}
			if sortedProviderConfigs[j].BudgetID != nil {
				bj = *sortedProviderConfigs[j].BudgetID
			}
			if bi != bj {
				return bi < bj
			}
			ri, rj := "", ""
			if sortedProviderConfigs[i].RateLimitID != nil {
				ri = *sortedProviderConfigs[i].RateLimitID
			}
			if sortedProviderConfigs[j].RateLimitID != nil {
				rj = *sortedProviderConfigs[j].RateLimitID
			}
			if ri != rj {
				return ri < rj
			}
			return getWeight(sortedProviderConfigs[i].Weight) < getWeight(sortedProviderConfigs[j].Weight)
		})
		// Filter out provider configs that are not available
		providerConfigsForHash := make([]VirtualKeyProviderConfigHashInput, len(sortedProviderConfigs))
		for i, pc := range sortedProviderConfigs {
			// Sort key IDs for deterministic hashing
			keyIDs := make([]string, len(pc.Keys))
			for j, k := range pc.Keys {
				keyIDs[j] = k.KeyID
			}
			sort.Strings(keyIDs)

			// Sort allowed models for deterministic hashing
			sortedAllowedModels := make([]string, len(pc.AllowedModels))
			copy(sortedAllowedModels, pc.AllowedModels)
			sort.Strings(sortedAllowedModels)
			providerConfigsForHash[i] = VirtualKeyProviderConfigHashInput{
				Provider:      pc.Provider,
				Weight:        getWeight(pc.Weight),
				AllowedModels: sortedAllowedModels,
				BudgetID:      pc.BudgetID,
				RateLimitID:   pc.RateLimitID,
				KeyIDs:        keyIDs,
			}
		}
		data, err := sonic.Marshal(providerConfigsForHash)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash MCPConfigs
	if len(vk.MCPConfigs) > 0 {
		// Copy and sort MCP configs for deterministic hashing
		sortedMCPConfigs := make([]tables.TableVirtualKeyMCPConfig, len(vk.MCPConfigs))
		copy(sortedMCPConfigs, vk.MCPConfigs)
		sort.Slice(sortedMCPConfigs, func(i, j int) bool {
			return sortedMCPConfigs[i].MCPClientID < sortedMCPConfigs[j].MCPClientID
		})

		mcpConfigsForHash := make([]VirtualKeyMCPConfigHashInput, len(sortedMCPConfigs))
		for i, mc := range sortedMCPConfigs {
			// Sort tools for deterministic hashing
			sortedTools := make([]string, len(mc.ToolsToExecute))
			copy(sortedTools, mc.ToolsToExecute)
			sort.Strings(sortedTools)

			mcpConfigsForHash[i] = VirtualKeyMCPConfigHashInput{
				MCPClientID:    mc.MCPClientID,
				ToolsToExecute: sortedTools,
			}
		}
		data, err := sonic.Marshal(mcpConfigsForHash)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateBudgetHash generates a SHA256 hash for a budget.
// This is used to detect changes to budgets between config.json and database.
// Skips: LastReset, CurrentUsage, CreatedAt, UpdatedAt (dynamic fields)
func GenerateBudgetHash(b tables.TableBudget) (string, error) {
	hash := sha256.New()

	// Hash ID
	hash.Write([]byte(b.ID))

	// Hash MaxLimit
	data, err := sonic.Marshal(b.MaxLimit)
	if err != nil {
		return "", err
	}
	hash.Write(data)

	// Hash ResetDuration
	hash.Write([]byte(b.ResetDuration))

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateRateLimitHash generates a SHA256 hash for a rate limit.
// This is used to detect changes to rate limits between config.json and database.
// Skips: CurrentUsage, LastReset, CreatedAt, UpdatedAt (dynamic fields)
func GenerateRateLimitHash(rl tables.TableRateLimit) (string, error) {
	hash := sha256.New()

	// Hash ID
	hash.Write([]byte(rl.ID))

	// Hash TokenMaxLimit
	if rl.TokenMaxLimit != nil {
		data, err := sonic.Marshal(*rl.TokenMaxLimit)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash TokenResetDuration
	if rl.TokenResetDuration != nil {
		hash.Write([]byte(*rl.TokenResetDuration))
	}

	// Hash RequestMaxLimit
	if rl.RequestMaxLimit != nil {
		data, err := sonic.Marshal(*rl.RequestMaxLimit)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash RequestResetDuration
	if rl.RequestResetDuration != nil {
		hash.Write([]byte(*rl.RequestResetDuration))
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateCustomerHash generates a SHA256 hash for a customer.
// This is used to detect changes to customers between config.json and database.
// Skips: CreatedAt, UpdatedAt, and relationship objects (dynamic fields)
func GenerateCustomerHash(c tables.TableCustomer) (string, error) {
	hash := sha256.New()

	// Hash ID
	hash.Write([]byte(c.ID))

	// Hash Name
	hash.Write([]byte(c.Name))

	// Hash BudgetID
	if c.BudgetID != nil {
		hash.Write([]byte("budgetID:" + *c.BudgetID))
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateTeamHash generates a SHA256 hash for a team.
// This is used to detect changes to teams between config.json and database.
// Skips: CreatedAt, UpdatedAt, and relationship objects (dynamic fields)
func GenerateTeamHash(t tables.TableTeam) (string, error) {
	hash := sha256.New()

	// Hash ID
	hash.Write([]byte(t.ID))

	// Hash Name
	hash.Write([]byte(t.Name))

	// Hash CustomerID
	if t.CustomerID != nil {
		hash.Write([]byte("customerID:" + *t.CustomerID))
	}

	// Hash BudgetID
	if t.BudgetID != nil {
		hash.Write([]byte("budgetID:" + *t.BudgetID))
	}

	// Hash Profile - use Profile if set, else marshal ParsedProfile
	// (Profile has json:"-" so when loading from JSON, only ParsedProfile is populated)
	// Use encoding/json for consistency with BeforeSave hook serialization
	if t.Profile != nil {
		hash.Write([]byte("profile:" + *t.Profile))
	} else if t.ParsedProfile != nil {
		data, err := json.Marshal(t.ParsedProfile)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("profile:" + string(data)))
	}

	// Hash Config - use Config if set, else marshal ParsedConfig
	// Use encoding/json for consistency with BeforeSave hook serialization
	if t.Config != nil {
		hash.Write([]byte("config:" + *t.Config))
	} else if t.ParsedConfig != nil {
		data, err := json.Marshal(t.ParsedConfig)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("config:" + string(data)))
	}

	// Hash Claims - use Claims if set, else marshal ParsedClaims
	// Use encoding/json for consistency with BeforeSave hook serialization
	if t.Claims != nil {
		hash.Write([]byte("claims:" + *t.Claims))
	} else if t.ParsedClaims != nil {
		data, err := json.Marshal(t.ParsedClaims)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("claims:" + string(data)))
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateMCPClientHash generates a SHA256 hash for an MCP client.
// This is used to detect changes to MCP clients between config.json and database.
// Skips: ID (autoIncrement), CreatedAt, UpdatedAt (dynamic fields)
func GenerateMCPClientHash(m tables.TableMCPClient) (string, error) {
	hash := sha256.New()

	// Hash ClientID
	hash.Write([]byte(m.ClientID))

	// Hash Name
	hash.Write([]byte(m.Name))

	// Hash ConnectionType
	hash.Write([]byte(m.ConnectionType))

	// Hash ConnectionString
	if m.ConnectionString != nil {
		if m.ConnectionString.IsFromEnv() {
			hash.Write([]byte(m.ConnectionString.EnvVar))
		} else {
			hash.Write([]byte(m.ConnectionString.Val))
		}
	}

	// Hash StdioConfig
	if m.StdioConfig != nil {
		data, err := sonic.Marshal(m.StdioConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash ToolsToExecute (sorted for deterministic hashing)
	if len(m.ToolsToExecute) > 0 {
		sortedTools := make([]string, len(m.ToolsToExecute))
		copy(sortedTools, m.ToolsToExecute)
		sort.Strings(sortedTools)
		data, err := sonic.Marshal(sortedTools)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash Headers (sorted for deterministic hashing)
	if len(m.Headers) > 0 {
		keys := make([]string, 0, len(m.Headers))
		for k := range m.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := m.Headers[k]
			if val.FromEnv {
				hash.Write([]byte(k + ":env:" + val.EnvVar))
			} else {
				hash.Write([]byte(k + ":val:" + val.Val))
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GeneratePluginHash generates a SHA256 hash for a plugin.
// This is used to detect changes to plugins between config.json and database.
// Skips: ID (autoIncrement), CreatedAt, UpdatedAt, IsCustom (dynamic fields)
func GeneratePluginHash(p tables.TablePlugin) (string, error) {
	hash := sha256.New()

	// Hash Name
	hash.Write([]byte(p.Name))

	// Hash Enabled
	if p.Enabled {
		hash.Write([]byte("enabled:true"))
	} else {
		hash.Write([]byte("enabled:false"))
	}

	// Hash Path
	if p.Path != nil {
		hash.Write([]byte("path:" + *p.Path))
	}

	// Hash Config (use ConfigJSON for consistent hashing)
	// Normalize: nil and empty map ({}) are treated as equivalent (no hash contribution)
	if p.ConfigJSON != "" && p.ConfigJSON != "{}" {
		hash.Write([]byte(p.ConfigJSON))
	} else if p.Config != nil {
		// Check if Config is a non-empty map before hashing
		// Use encoding/json for consistency with BeforeSave hook serialization
		data, err := json.Marshal(p.Config)
		if err != nil {
			return "", err
		}
		// Only hash if it's not an empty object
		if string(data) != "{}" && string(data) != "null" {
			hash.Write(data)
		}
	}

	// Hash Version
	data, err := sonic.Marshal(p.Version)
	if err != nil {
		return "", err
	}
	hash.Write(data)

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// AuthConfig represents configured auth config for Bifrost dashboard
type AuthConfig struct {
	AdminUserName          string `json:"admin_username"`
	AdminPassword          string `json:"admin_password"`
	IsEnabled              bool   `json:"is_enabled"`
	DisableAuthOnInference bool   `json:"disable_auth_on_inference"`
}

// ConfigMap maps provider names to their configurations.
type ConfigMap map[schemas.ModelProvider]ProviderConfig

type GovernanceConfig struct {
	VirtualKeys  []tables.TableVirtualKey  `json:"virtual_keys"`
	Teams        []tables.TableTeam        `json:"teams"`
	Customers    []tables.TableCustomer    `json:"customers"`
	Budgets      []tables.TableBudget      `json:"budgets"`
	RateLimits   []tables.TableRateLimit   `json:"rate_limits"`
	ModelConfigs []tables.TableModelConfig `json:"model_configs"`
	Providers    []tables.TableProvider    `json:"providers"`
	AuthConfig   *AuthConfig               `json:"auth_config,omitempty"`
}
