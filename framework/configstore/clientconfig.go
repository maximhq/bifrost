package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"maps"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
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
	SecretVar  string                // The environment variable name (without env. prefix)
	Provider   schemas.ModelProvider // The provider this key belongs to (empty for core/mcp configs)
	KeyType    EnvKeyType            // Type of key (e.g., "api_key", "azure_config", "vertex_config", "bedrock_config", "connection_string", "mcp_header")
	ConfigPath string                // Path in config where this env var is used
	KeyID      string                // The key ID this env var belongs to (empty for non-key configs like bedrock_config, connection_string)
}

// CompatConfig holds the compat plugin feature flags.
type CompatConfig struct {
	ConvertTextToChat      bool `json:"convert_text_to_chat"`
	ConvertChatToResponses bool `json:"convert_chat_to_responses"`
	ShouldDropParams       bool `json:"should_drop_params"`
	ShouldConvertParams    bool `json:"should_convert_params"`
}

// UnmarshalJSON defaults all bool fields to true when absent from JSON.
func (c *CompatConfig) UnmarshalJSON(data []byte) error {
	type compatConfig struct {
		ConvertTextToChat      *bool `json:"convert_text_to_chat"`
		ConvertChatToResponses *bool `json:"convert_chat_to_responses"`
		ShouldDropParams       *bool `json:"should_drop_params"`
		ShouldConvertParams    *bool `json:"should_convert_params"`
	}
	var s compatConfig
	if err := sonic.Unmarshal(data, &s); err != nil {
		return err
	}
	c.ConvertTextToChat = s.ConvertTextToChat == nil || *s.ConvertTextToChat
	c.ConvertChatToResponses = s.ConvertChatToResponses == nil || *s.ConvertChatToResponses
	c.ShouldDropParams = s.ShouldDropParams == nil || *s.ShouldDropParams
	c.ShouldConvertParams = s.ShouldConvertParams == nil || *s.ShouldConvertParams
	return nil
}

// ClientConfig represents the core configuration for Bifrost HTTP transport and the Bifrost Client.
// It includes settings for excess request handling, Prometheus metrics, and initial pool size.
type ClientConfig struct {
	DropExcessRequests                    bool                                  `json:"drop_excess_requests"`                       // Drop excess requests if the provider queue is full
	InitialPoolSize                       int                                   `json:"initial_pool_size"`                          // The initial pool size for the bifrost client
	PrometheusLabels                      []string                              `json:"prometheus_labels"`                          // The labels to be used for prometheus metrics
	EnableLogging                         *bool                                 `json:"enable_logging"`                             // Enable logging of requests and responses
	DisableContentLogging                 bool                                  `json:"disable_content_logging"`                    // Disable logging of content
	RetainContentInObjectStorage          bool                                  `json:"retain_content_in_object_storage"`           // When content logging is disabled (config or header), still offload content to object storage as hidden instead of dropping it
	AllowPerRequestContentStorageOverride bool                                  `json:"allow_per_request_content_storage_override"` // Allow per-request override of content storage via x-bf-disable-content-logging header/context
	AllowPerRequestRawOverride            bool                                  `json:"allow_per_request_raw_override"`             // Allow per-request override of raw request/response visibility via x-bf-send-back-raw-request and x-bf-send-back-raw-response headers
	AllowDirectKeys                       bool                                  `json:"allow_direct_keys"`                          // Allow callers to bypass the registered key pool via x-bf-direct-key: true header
	DisableDBPingsInHealth                bool                                  `json:"disable_db_pings_in_health"`
	LogRetentionDays                      int                                   `json:"log_retention_days" validate:"min=1"`         // Number of days to retain logs (minimum 1 day)
	EnforceAuthOnInference                bool                                  `json:"enforce_auth_on_inference"`                   // Require auth (VK, API key, or user token) on inference endpoints
	DualCredentialConflictBehavior        tables.DualCredentialConflictBehavior `json:"dual_credential_conflict_behavior,omitempty"` // Behavior when both an IDP token and a VK are present on an inference request
	EnforceGovernanceHeader               bool                                  `json:"enforce_governance_header,omitempty"`         // Deprecated: use EnforceAuthOnInference
	EnforceSCIMAuth                       bool                                  `json:"enforce_scim_auth,omitempty"`                 // Deprecated: use EnforceAuthOnInference
	AllowedOrigins                        []string                              `json:"allowed_origins,omitempty"`                   // Additional allowed origins for CORS and WebSocket (localhost is always allowed)
	AllowedHeaders                        []string                              `json:"allowed_headers,omitempty"`                   // Additional allowed headers for CORS and WebSocket
	MaxRequestBodySizeMB                  int                                   `json:"max_request_body_size_mb"`                    // The maximum request body size in MB
	Compat                                CompatConfig                          `json:"compat"`                                      // Compat plugin configuration
	MCPAgentDepth                         int                                   `json:"mcp_agent_depth"`                             // The maximum depth for MCP agent mode tool execution
	MCPToolExecutionTimeout               int                                   `json:"mcp_tool_execution_timeout"`                  // The timeout for individual tool execution in seconds
	MCPCodeModeBindingLevel               string                                `json:"mcp_code_mode_binding_level"`                 // Code mode binding level: "server" or "tool"
	MCPToolSyncInterval                   int                                   `json:"mcp_tool_sync_interval"`                      // Global tool sync interval in minutes (default: 10, 0 = disabled)
	MCPDisableAutoToolInject              bool                                  `json:"mcp_disable_auto_tool_inject"`                // When true, MCP tools are not injected into requests by default
	MCPEnableTempTokenAuth                bool                                  `json:"mcp_enable_temp_token_auth"`                  // When true, scoped temp tokens can authorize MCP per-user OAuth and per-user-headers auth pages. User-mode flows never mint regardless.
	HeaderFilterConfig                    *tables.GlobalHeaderFilterConfig      `json:"header_filter_config,omitempty"`              // Global header filtering configuration for x-bf-eh-* headers
	AsyncJobResultTTL                     int                                   `json:"async_job_result_ttl"`                        // Default TTL for async job results in seconds (default: 3600 = 1 hour)
	RequiredHeaders                       []string                              `json:"required_headers,omitempty"`                  // Headers that must be present on every request (case-insensitive)
	LoggingHeaders                        []string                              `json:"logging_headers,omitempty"`                   // Headers to capture in log metadata
	WhitelistedRoutes                     []string                              `json:"whitelisted_routes,omitempty"`                // Routes that bypass auth middleware
	HideDeletedVirtualKeysInFilters       bool                                  `json:"hide_deleted_virtual_keys_in_filters"`        // Hide deleted virtual keys from logs/MCP filter data
	RoutingChainMaxDepth                  int                                   `json:"routing_chain_max_depth"`                     // Maximum depth for routing rule chain evaluation (default: 10)
	MCPExternalClientURL                  *schemas.SecretVar                    `json:"mcp_external_client_url,omitempty"`           // Public base URL used as redirect_uri when Bifrost acts as an OAuth client to upstream MCP servers. Supports env var syntax ("env.MY_VAR")
	MCPServerAuthMode                     tables.MCPServerAuthMode              `json:"mcp_server_auth_mode,omitempty"`              // How /mcp authenticates inbound clients: headers (default), both, or oauth.
	OAuth2ServerConfig                    *tables.OAuth2ServerConfig            `json:"oauth2_server_config,omitempty"`              // OAuth2 AS-specific settings (IssuerURL, token TTLs). Only relevant when MCPServerAuthMode is both or oauth.
	ConfigHash                            string                                `json:"-"`                                           // Config hash for reconciliation (not serialized)
	DumpErrorsInConsoleLogs               bool                                  `json:"dump_errors_in_console_logs"`                 // Dump error details in console logs
	WebhookConfig                         *tables.WebhookConfig                 `json:"webhook_config,omitempty"`                    // Global webhook delivery settings; nil means all defaults
}

// IsMCPOAuthDiscoveryEnabled reports whether the well-known OAuth discovery
// endpoints and JWKS endpoint should be live. True when MCPServerAuthMode is
// both or oauth.
func (c *ClientConfig) IsMCPOAuthDiscoveryEnabled() bool {
	return c.MCPServerAuthMode == tables.MCPServerAuthModeBoth || c.MCPServerAuthMode == tables.MCPServerAuthModeOAuth
}

// UnmarshalJSON defaults all bool fields to true when absent from JSON.
func (c *ClientConfig) UnmarshalJSON(data []byte) error {
	type ClientConfigAlias ClientConfig
	alias := ClientConfigAlias{
		Compat: CompatConfig{
			ConvertTextToChat:      true,
			ConvertChatToResponses: true,
			ShouldDropParams:       true,
			ShouldConvertParams:    true,
		},
	}
	if err := sonic.Unmarshal(data, &alias); err != nil {
		return err
	}
	*c = ClientConfig(alias)
	return nil
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

	enableLogging := c.EnableLogging == nil || *c.EnableLogging
	if enableLogging {
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

	if c.EnforceAuthOnInference {
		hash.Write([]byte("enforceAuthOnInference:true"))
	} else {
		hash.Write([]byte("enforceAuthOnInference:false"))
	}

	if c.DualCredentialConflictBehavior != "" && c.DualCredentialConflictBehavior != tables.DualCredentialConflictBehaviorPreferIDP {
		hash.Write([]byte("dualCredentialConflictBehavior:" + string(c.DualCredentialConflictBehavior)))
	}

	if c.Compat.ConvertTextToChat {
		hash.Write([]byte("compatConvertTextToChat:true"))
	}
	if c.Compat.ConvertChatToResponses {
		hash.Write([]byte("compatConvertChatToResponses:true"))
	}
	if c.Compat.ShouldDropParams {
		hash.Write([]byte("compatShouldDropParams:true"))
	}
	if c.Compat.ShouldConvertParams {
		hash.Write([]byte("compatShouldConvertParams:true"))
	}

	// Only hash non-default value to avoid legacy config hash churn.
	if c.HideDeletedVirtualKeysInFilters {
		hash.Write([]byte("hideDeletedVirtualKeysInFilters:true"))
	}

	// Always hash when non-zero — explicitly setting the default (10) is a meaningful
	// config change that should be reflected in the hash. The migration that introduces
	// this field backfills existing rows with RoutingChainMaxDepth=10 and regenerates
	// their config_hash so there is no hash churn on upgrade for unmodified configs.
	if c.RoutingChainMaxDepth > 0 {
		hash.Write([]byte("routingChainMaxDepth:" + strconv.Itoa(c.RoutingChainMaxDepth)))
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

	// Only hash non-default value to avoid legacy config hash churn on upgrade.
	if c.MCPDisableAutoToolInject {
		hash.Write([]byte("mcpDisableAutoToolInject:true"))
	}

	// Only hash non-default value to avoid legacy config hash churn on upgrade.
	if c.MCPEnableTempTokenAuth {
		hash.Write([]byte("mcpEnableTempTokenAuth:true"))
	}

	// Only hash non-default value to avoid legacy config hash churn on upgrade.
	if c.AllowPerRequestContentStorageOverride {
		hash.Write([]byte("allowPerRequestContentStorageOverride:true"))
	}

	// Only hash non-default value to avoid legacy config hash churn on upgrade.
	if c.RetainContentInObjectStorage {
		hash.Write([]byte("retainContentInObjectStorage:true"))
	}

	if c.AllowPerRequestRawOverride {
		hash.Write([]byte("allowPerRequestRawOverride:true"))
	}

	// Only hash non-default value to avoid legacy config hash churn on upgrade.
	if c.AllowDirectKeys {
		hash.Write([]byte("allowDirectKeys:true"))
	}

	if c.AsyncJobResultTTL > 0 {
		hash.Write([]byte("asyncJobResultTTL:" + strconv.Itoa(c.AsyncJobResultTTL)))
	} else {
		hash.Write([]byte("asyncJobResultTTL:0"))
	}

	// Only hash non-default value to avoid legacy config hash churn on upgrade.
	if c.DumpErrorsInConsoleLogs {
		hash.Write([]byte("dumpErrorsInConsoleLogs:true"))
	}

	// Only hash when present to avoid legacy config hash churn on upgrade.
	if c.WebhookConfig != nil {
		data, err := sonic.Marshal(c.WebhookConfig)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("webhookConfig:"))
		hash.Write(data)
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

	// Hash LoggingHeaders (sorted for deterministic hashing)
	if len(c.LoggingHeaders) > 0 {
		sortedLogging := make([]string, len(c.LoggingHeaders))
		copy(sortedLogging, c.LoggingHeaders)
		sort.Strings(sortedLogging)
		data, err := sonic.Marshal(sortedLogging)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("loggingHeaders:"))
		hash.Write(data)
	}

	// Hash RequiredHeaders (sorted for deterministic hashing)
	if len(c.RequiredHeaders) > 0 {
		sortedRequired := make([]string, len(c.RequiredHeaders))
		copy(sortedRequired, c.RequiredHeaders)
		sort.Strings(sortedRequired)
		data, err := sonic.Marshal(sortedRequired)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("requiredHeaders:"))
		hash.Write(data)
	}

	// Hash WhitelistedRoutes (sorted for deterministic hashing)
	if len(c.WhitelistedRoutes) > 0 {
		sortedRoutes := make([]string, len(c.WhitelistedRoutes))
		copy(sortedRoutes, c.WhitelistedRoutes)
		sort.Strings(sortedRoutes)
		data, err := sonic.Marshal(sortedRoutes)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("whitelistedRoutes:"))
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

	if c.MCPExternalClientURL.IsSet() {
		if c.MCPExternalClientURL.IsFromSecret() {
			hash.Write([]byte("externalClientURL:ref:" + c.MCPExternalClientURL.GetRawRef()))
		} else {
			hash.Write([]byte("externalClientURL:val:" + c.MCPExternalClientURL.GetValue()))
		}
	}

	// Only hash non-default values to avoid legacy config hash churn on upgrade —
	// existing configs carry an empty auth mode and a nil OAuth2 server config.
	if c.MCPServerAuthMode != "" {
		hash.Write([]byte("mcpServerAuthMode:" + string(c.MCPServerAuthMode)))
	}
	// Hash OAuth2ServerConfig field-by-field (not via Marshal) for a stable,
	// deterministic byte stream that does not depend on serializer field order.
	if c.OAuth2ServerConfig != nil {
		oc := c.OAuth2ServerConfig
		if oc.IssuerURL.IsSet() {
			if oc.IssuerURL.IsFromEnv() {
				hash.Write([]byte("oauth2IssuerURL:env:" + oc.IssuerURL.GetRawRef()))
			} else {
				hash.Write([]byte("oauth2IssuerURL:val:" + oc.IssuerURL.GetValue()))
			}
		}
		hash.Write([]byte("oauth2AuthCodeTTL:" + strconv.Itoa(oc.AuthCodeTTL)))
		hash.Write([]byte("oauth2AccessTokenTTL:" + strconv.Itoa(oc.AccessTokenTTL)))
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateClientConfigHashWithToolManager extends GenerateClientConfigHash to also cover
// the mcp.tool_manager_config file section. When tm is nil it returns the same value as
// GenerateClientConfigHash, so it is safe to call unconditionally.
func (c *ClientConfig) GenerateClientConfigHashWithToolManager(tm *schemas.MCPToolManagerConfig) (string, error) {
	base, err := c.GenerateClientConfigHash()
	if err != nil || tm == nil {
		return base, err
	}
	h := sha256.New()
	h.Write([]byte(base))
	h.Write([]byte("toolMgrAgentDepth:" + strconv.Itoa(tm.MaxAgentDepth)))
	h.Write([]byte("toolMgrTimeout:" + strconv.FormatInt(int64(math.Ceil(tm.ToolExecutionTimeout.D().Seconds())), 10)))
	h.Write([]byte("toolMgrCodeMode:" + string(tm.CodeModeBindingLevel)))
	if tm.DisableAutoToolInject {
		h.Write([]byte("toolMgrDisableAutoInject:true"))
	} else {
		h.Write([]byte("toolMgrDisableAutoInject:false"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Redacted returns a copy of ClientConfig with any env-backed SecretVar fields masked.
func (c *ClientConfig) Redacted() ClientConfig {
	out := *c
	if c.MCPExternalClientURL != nil && c.MCPExternalClientURL.IsFromSecret() {
		out.MCPExternalClientURL = c.MCPExternalClientURL.Redacted()
	}
	return out
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
	StoreRawRequestResponse  bool                              `json:"store_raw_request_response"`            // Capture raw request/response for internal logging only; strip from API responses returned to clients
	CustomProviderConfig     *schemas.CustomProviderConfig     `json:"custom_provider_config,omitempty"`      // Custom provider configuration
	OpenAIConfig             *schemas.OpenAIConfig             `json:"openai_config,omitempty"`               // OpenAI-specific configuration
	ConfigHash               string                            `json:"config_hash,omitempty"`                 // Hash of config.json version, used for change detection
	Status                   string                            `json:"status,omitempty"`                      // Model discovery status for keyless providers
	Description              string                            `json:"description,omitempty"`                 // Model discovery error message for keyless providers
}

// Redacted returns a redacted copy of the provider configuration.
func (p *ProviderConfig) Redacted() *ProviderConfig {
	// Create redacted config with same structure but redacted values
	var redactedNetworkConfig *schemas.NetworkConfig
	if p.NetworkConfig != nil {
		redactedNetworkConfig = p.NetworkConfig.Redacted()
	}
	redactedConfig := ProviderConfig{
		NetworkConfig:            redactedNetworkConfig,
		ConcurrencyAndBufferSize: p.ConcurrencyAndBufferSize,
		SendBackRawRequest:       p.SendBackRawRequest,
		SendBackRawResponse:      p.SendBackRawResponse,
		StoreRawRequestResponse:  p.StoreRawRequestResponse,
		CustomProviderConfig:     p.CustomProviderConfig,
		OpenAIConfig:             p.OpenAIConfig,
		ConfigHash:               p.ConfigHash,
		Status:                   p.Status,
		Description:              p.Description,
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
		blacklistedModels := key.BlacklistedModels
		if blacklistedModels == nil {
			blacklistedModels = []string{} // Match models: empty JSON array, not null
		}
		redactedConfig.Keys[i] = schemas.Key{
			ID:                key.ID,
			Name:              key.Name,
			Models:            models,
			BlacklistedModels: blacklistedModels,
			Weight:            key.Weight,
			ConfigHash:        key.ConfigHash,
		}
		if key.Enabled != nil {
			enabled := *key.Enabled
			redactedConfig.Keys[i].Enabled = &enabled
		}
		if key.Aliases != nil {
			redactedConfig.Keys[i].Aliases = maps.Clone(key.Aliases)
		}
		redactedConfig.Keys[i].Value = *key.Value.Redacted()
		// Add back use for batch api
		if key.UseForBatchAPI != nil {
			redactedConfig.Keys[i].UseForBatchAPI = key.UseForBatchAPI
		} else {
			redactedConfig.Keys[i].UseForBatchAPI = new(false)
		}
		// Add back use anthropic endpoints
		if key.UseAnthropicEndpoints != nil {
			redactedConfig.Keys[i].UseAnthropicEndpoints = key.UseAnthropicEndpoints
		} else {
			redactedConfig.Keys[i].UseAnthropicEndpoints = new(false)
		}

		// Add model discovery status and error
		redactedConfig.Keys[i].Status = key.Status
		redactedConfig.Keys[i].Description = key.Description

		// Redact Azure key config if present
		if key.AzureKeyConfig != nil {
			azureConfig := &schemas.AzureKeyConfig{}
			if key.AzureKeyConfig.Endpoint.IsFromSecret() {
				azureConfig.Endpoint = *key.AzureKeyConfig.Endpoint.Redacted()
			} else {
				azureConfig.Endpoint = key.AzureKeyConfig.Endpoint
			}
			if key.AzureKeyConfig.ClientID != nil {
				azureConfig.ClientID = key.AzureKeyConfig.ClientID.Redacted()
			}
			if key.AzureKeyConfig.ClientSecret != nil {
				azureConfig.ClientSecret = key.AzureKeyConfig.ClientSecret.Redacted()
			}
			if key.AzureKeyConfig.TenantID != nil {
				azureConfig.TenantID = key.AzureKeyConfig.TenantID.Redacted()
			}
			if len(key.AzureKeyConfig.Scopes) > 0 {
				azureConfig.Scopes = key.AzureKeyConfig.Scopes
			}
			redactedConfig.Keys[i].AzureKeyConfig = azureConfig
		}

		// Redact Vertex key config if present
		if key.VertexKeyConfig != nil {
			vertexConfig := &schemas.VertexKeyConfig{}
			vertexConfig.ProjectID = *key.VertexKeyConfig.ProjectID.Redacted()
			vertexConfig.ProjectNumber = *key.VertexKeyConfig.ProjectNumber.Redacted()
			vertexConfig.Region = *key.VertexKeyConfig.Region.Redacted()
			vertexConfig.AuthCredentials = *key.VertexKeyConfig.AuthCredentials.Redacted()
			vertexConfig.ForceSingleRegion = key.VertexKeyConfig.ForceSingleRegion
			redactedConfig.Keys[i].VertexKeyConfig = vertexConfig
		}

		// Redact Bedrock key config if present
		if key.BedrockKeyConfig != nil {
			bedrockConfig := &schemas.BedrockKeyConfig{}
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
			if key.BedrockKeyConfig.RoleARN != nil {
				bedrockConfig.RoleARN = key.BedrockKeyConfig.RoleARN.Redacted()
			}
			if key.BedrockKeyConfig.ExternalID != nil {
				bedrockConfig.ExternalID = key.BedrockKeyConfig.ExternalID.Redacted()
			}
			if key.BedrockKeyConfig.RoleSessionName != nil {
				bedrockConfig.RoleSessionName = key.BedrockKeyConfig.RoleSessionName.Redacted()
			}
			if key.BedrockKeyConfig.BatchRoleARN != nil {
				bedrockConfig.BatchRoleARN = key.BedrockKeyConfig.BatchRoleARN.Redacted()
			}
			// Mantle project ID is an identifier, not a credential — surface it in plaintext.
			if key.BedrockKeyConfig.ProjectID != nil {
				bedrockConfig.ProjectID = key.BedrockKeyConfig.ProjectID
			}
			// Add back s3 config
			if key.BedrockKeyConfig.BatchS3Config != nil {
				bedrockConfig.BatchS3Config = key.BedrockKeyConfig.BatchS3Config
			}
			redactedConfig.Keys[i].BedrockKeyConfig = bedrockConfig
		}

		// Redact Bedrock Mantle key config if present
		if key.BedrockMantleKeyConfig != nil {
			mantleConfig := &schemas.BedrockMantleKeyConfig{}
			mantleConfig.AccessKey = *key.BedrockMantleKeyConfig.AccessKey.Redacted()
			mantleConfig.SecretKey = *key.BedrockMantleKeyConfig.SecretKey.Redacted()
			if key.BedrockMantleKeyConfig.SessionToken != nil {
				mantleConfig.SessionToken = key.BedrockMantleKeyConfig.SessionToken.Redacted()
			}
			if key.BedrockMantleKeyConfig.Region != nil {
				mantleConfig.Region = key.BedrockMantleKeyConfig.Region.Redacted()
			}
			if key.BedrockMantleKeyConfig.RoleARN != nil {
				mantleConfig.RoleARN = key.BedrockMantleKeyConfig.RoleARN.Redacted()
			}
			if key.BedrockMantleKeyConfig.ExternalID != nil {
				mantleConfig.ExternalID = key.BedrockMantleKeyConfig.ExternalID.Redacted()
			}
			if key.BedrockMantleKeyConfig.RoleSessionName != nil {
				mantleConfig.RoleSessionName = key.BedrockMantleKeyConfig.RoleSessionName.Redacted()
			}
			// Project ID is an identifier, not a credential — surface it in plaintext.
			if key.BedrockMantleKeyConfig.ProjectID != nil {
				mantleConfig.ProjectID = key.BedrockMantleKeyConfig.ProjectID
			}
			redactedConfig.Keys[i].BedrockMantleKeyConfig = mantleConfig
		}

		if key.VLLMKeyConfig != nil {
			vllmConfig := &schemas.VLLMKeyConfig{
				ModelName: key.VLLMKeyConfig.ModelName,
			}
			vllmConfig.URL = *key.VLLMKeyConfig.URL.Redacted()
			redactedConfig.Keys[i].VLLMKeyConfig = vllmConfig
		}

		if key.ReplicateKeyConfig != nil {
			replicateConfig := &schemas.ReplicateKeyConfig{
				UseDeploymentsEndpoint: key.ReplicateKeyConfig.UseDeploymentsEndpoint,
			}
			redactedConfig.Keys[i].ReplicateKeyConfig = replicateConfig
		}

		if key.OllamaKeyConfig != nil {
			ollamaConfig := &schemas.OllamaKeyConfig{}
			ollamaConfig.URL = *key.OllamaKeyConfig.URL.Redacted()
			redactedConfig.Keys[i].OllamaKeyConfig = ollamaConfig
		}

		if key.SGLKeyConfig != nil {
			sglConfig := &schemas.SGLKeyConfig{}
			sglConfig.URL = *key.SGLKeyConfig.URL.Redacted()
			redactedConfig.Keys[i].SGLKeyConfig = sglConfig
		}

		if key.GigaChatKeyConfig != nil {
			redactedConfig.Keys[i].GigaChatKeyConfig = key.GigaChatKeyConfig.Redacted()
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

	// Hash OpenAIConfig
	if p.OpenAIConfig != nil {
		data, err := sonic.Marshal(p.OpenAIConfig)
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

	// Hash StoreRawRequestResponse
	if p.StoreRawRequestResponse {
		hash.Write([]byte("storeRawRequestResponse"))
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
	// Hash Value (prefix with source type to prevent collisions between ref and literal)
	if key.Value.IsFromSecret() {
		hash.Write([]byte("ref:" + key.Value.GetRawRef()))
	} else {
		hash.Write([]byte("val:" + key.Value.Val))
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
	// Hash BlacklistedModels (key-level deny list)
	if len(key.BlacklistedModels) > 0 {
		sortedBlacklistedModels := make([]string, len(key.BlacklistedModels))
		copy(sortedBlacklistedModels, key.BlacklistedModels)
		sort.Strings(sortedBlacklistedModels)
		data, err := sonic.Marshal(sortedBlacklistedModels)
		if err != nil {
			return "", err
		}
		hash.Write([]byte("blacklistedModels:"))
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
	// Hash BedrockMantleKeyConfig
	if key.BedrockMantleKeyConfig != nil {
		data, err := sonic.Marshal(key.BedrockMantleKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash Aliases
	if key.Aliases != nil {
		data, err := sonic.Marshal(key.Aliases)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash VLLMKeyConfig
	if key.VLLMKeyConfig != nil {
		data, err := sonic.Marshal(key.VLLMKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash ReplicateKeyConfig
	if key.ReplicateKeyConfig != nil {
		data, err := sonic.Marshal(key.ReplicateKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash OllamaKeyConfig
	if key.OllamaKeyConfig != nil {
		data, err := sonic.Marshal(key.OllamaKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash SGLKeyConfig
	if key.SGLKeyConfig != nil {
		data, err := sonic.Marshal(key.SGLKeyConfig)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}
	// Hash GigaChatKeyConfig
	if key.GigaChatKeyConfig != nil {
		data, err := sonic.Marshal(key.GigaChatKeyConfig)
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
	// Hash UseAnthropicEndpoints (nil = default false for new keys)
	useAnthropicEndpoints := false
	if key.UseAnthropicEndpoints != nil {
		useAnthropicEndpoints = *key.UseAnthropicEndpoints
	}
	if useAnthropicEndpoints {
		hash.Write([]byte("useAnthropicEndpoints:true"))
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
	RateLimitID *string
	// ProviderConfigs and MCPConfigs are hashed separately as they contain nested data
	ProviderConfigs []VirtualKeyProviderConfigHashInput
	MCPConfigs      []VirtualKeyMCPConfigHashInput
}

// VirtualKeyProviderConfigHashInput represents provider config fields for hashing
type VirtualKeyProviderConfigHashInput struct {
	Provider          string
	Weight            *float64
	AllowedModels     []string
	BlacklistedModels []string
	AllowAllKeys      bool
	RateLimitID       *string
	KeyIDs            []string // Only key IDs, not full key objects
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
	// Hash the resolved value so that secret rotation (vault/env change) is
	// detected as a config change and triggers a re-sync.
	hash.Write([]byte(vk.Value.GetValue()))
	// Hash IsActive (nil treated as DB default true)
	if vk.IsActiveValue() {
		hash.Write([]byte("isActive:true"))
	} else {
		hash.Write([]byte("isActive:false"))
	}
	// Hash ExpiresAt only when set, so rows created before expiry existed keep their hash
	if vk.ExpiresAt != nil {
		hash.Write([]byte("expiresAt:" + vk.ExpiresAt.UTC().Format(time.RFC3339Nano)))
	}
	// Hash TeamID
	if vk.TeamID != nil {
		hash.Write([]byte("teamID:" + *vk.TeamID))
	}
	// Hash CustomerID
	if vk.CustomerID != nil {
		hash.Write([]byte("customerID:" + *vk.CustomerID))
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
			wi, wj := sortedProviderConfigs[i].Weight, sortedProviderConfigs[j].Weight
			if (wi == nil) != (wj == nil) {
				return wi == nil
			}
			if wi != nil && wj != nil && *wi != *wj {
				return *wi < *wj
			}
			return false
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

			// Sort blacklisted models for deterministic hashing
			sortedBlacklistedModels := make([]string, len(pc.BlacklistedModels))
			copy(sortedBlacklistedModels, pc.BlacklistedModels)
			sort.Strings(sortedBlacklistedModels)
			providerConfigsForHash[i] = VirtualKeyProviderConfigHashInput{
				Provider:          pc.Provider,
				Weight:            pc.Weight,
				AllowedModels:     sortedAllowedModels,
				BlacklistedModels: sortedBlacklistedModels,
				AllowAllKeys:      pc.AllowAllKeys,
				RateLimitID:       pc.RateLimitID,
				KeyIDs:            keyIDs,
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

	// Collect budget IDs from both sources so config-file context (BudgetID) and
	// DB context (Budgets) produce the same hash for the same logical state.
	seen := make(map[string]bool, len(c.Budgets)+1)
	if c.BudgetID != nil {
		seen[*c.BudgetID] = true
	}
	for _, b := range c.Budgets {
		seen[b.ID] = true
	}
	budgetIDs := make([]string, 0, len(seen))
	for id := range seen {
		budgetIDs = append(budgetIDs, id)
	}
	sort.Strings(budgetIDs)
	for _, id := range budgetIDs {
		hash.Write([]byte("budgetID:" + id))
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

	// Hash sorted budget IDs — team now owns multiple budgets; slice order must not
	// affect the hash, otherwise config-sync would flip the hash on every reload.
	if len(t.Budgets) > 0 {
		ids := make([]string, len(t.Budgets))
		for i, b := range t.Budgets {
			ids[i] = b.ID
		}
		sort.Strings(ids)
		hash.Write([]byte("budgetIDs:"))
		for i, id := range ids {
			if i > 0 {
				hash.Write([]byte{','})
			}
			hash.Write([]byte(id))
		}
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

// GenerateModelConfigHash generates a SHA256 hash for a model config.
// This is used to detect changes to model configs between config.json and database.
// Skips: CreatedAt, UpdatedAt, and relationship objects (dynamic fields)
func GenerateModelConfigHash(m tables.TableModelConfig) (string, error) {
	// Normalize an empty scope to "global" so a config.json entry that omits scope
	// hashes identically to the defaulted DB row.
	scope := m.Scope
	if scope == "" {
		scope = tables.ModelConfigScopeGlobal
	}
	hash := sha256.New()
	writeHashField(hash, "id", m.ID)
	writeHashField(hash, "model_name", m.ModelName)
	writeHashField(hash, "provider", derefStr(m.Provider))
	writeHashField(hash, "scope", scope)
	writeHashField(hash, "scope_id", derefStr(m.ScopeID))
	writeHashField(hash, "budget_id", derefStr(m.BudgetID))
	writeHashField(hash, "rate_limit_id", derefStr(m.RateLimitID))
	sortedBudgetIDs := append([]string(nil), m.BudgetIDs...)
	sort.Strings(sortedBudgetIDs)
	for _, id := range sortedBudgetIDs {
		writeHashField(hash, "budget_ids", id)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateProviderGovernanceHash generates a SHA256 hash for provider-governance
// bindings only (provider name + budget/rate-limit references).
// It intentionally excludes provider runtime/config fields and keys.
func GenerateProviderGovernanceHash(p tables.TableProvider) (string, error) {
	hash := sha256.New()
	writeHashField(hash, "name", p.Name)
	writeHashField(hash, "budget_id", derefStr(p.BudgetID))
	writeHashField(hash, "rate_limit_id", derefStr(p.RateLimitID))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateComplexityAnalyzerConfigHashes returns stable section hashes for
// config.json-sourced analyzer config.
func GenerateComplexityAnalyzerConfigHashes(config *ComplexityAnalyzerConfig) (ComplexityAnalyzerConfigHashes, error) {
	if config == nil {
		return ComplexityAnalyzerConfigHashes{}, fmt.Errorf("complexity analyzer config is nil")
	}

	normalized := config.Normalized()
	normalized.ConfigHashes = ComplexityAnalyzerConfigHashes{}
	if err := normalized.Validate(); err != nil {
		return ComplexityAnalyzerConfigHashes{}, err
	}

	tierHash, err := hashComplexityValue(normalized.TierBoundaries)
	if err != nil {
		return ComplexityAnalyzerConfigHashes{}, fmt.Errorf("failed to hash tier boundaries: %w", err)
	}
	codeHash, err := hashComplexityValue(normalized.Keywords.CodeKeywords)
	if err != nil {
		return ComplexityAnalyzerConfigHashes{}, fmt.Errorf("failed to hash code keywords: %w", err)
	}
	reasoningHash, err := hashComplexityValue(normalized.Keywords.ReasoningKeywords)
	if err != nil {
		return ComplexityAnalyzerConfigHashes{}, fmt.Errorf("failed to hash reasoning keywords: %w", err)
	}
	technicalHash, err := hashComplexityValue(normalized.Keywords.TechnicalKeywords)
	if err != nil {
		return ComplexityAnalyzerConfigHashes{}, fmt.Errorf("failed to hash technical keywords: %w", err)
	}
	simpleHash, err := hashComplexityValue(normalized.Keywords.SimpleKeywords)
	if err != nil {
		return ComplexityAnalyzerConfigHashes{}, fmt.Errorf("failed to hash simple keywords: %w", err)
	}

	return ComplexityAnalyzerConfigHashes{
		TierBoundaries:    tierHash,
		CodeKeywords:      codeHash,
		ReasoningKeywords: reasoningHash,
		TechnicalKeywords: technicalHash,
		SimpleKeywords:    simpleHash,
	}, nil
}

func hashComplexityValue(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// writeHashField writes a field identifier and a length-prefixed value so the
// resulting byte stream is unambiguous and cannot collide via concatenation.
func writeHashField(hash hash.Hash, fieldID, value string) {
	hash.Write([]byte(fieldID))
	hash.Write([]byte(":"))
	hash.Write([]byte(strconv.Itoa(len(value))))
	hash.Write([]byte(":"))
	hash.Write([]byte(value))
	hash.Write([]byte(";"))
}

// GenerateRoutingRuleHash generates a SHA256 hash for a routing rule.
// This is used to detect changes to routing rules between config.json and database.
// routingTargetHashPayload is a canonical struct for hashing a routing target.
// Used to ensure deterministic hashes regardless of slice order.
// Fields use plain string (not *string) so nil and "" both marshal to "" and produce the same hash.
type routingTargetHashPayload struct {
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	KeyID    string  `json:"key_id"`
	Weight   float64 `json:"weight"`
}

// derefStr returns the dereferenced value of s, or "" if s is nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Skips: CreatedAt, UpdatedAt (dynamic fields)
func GenerateRoutingRuleHash(r tables.TableRoutingRule) (string, error) {
	hash := sha256.New()

	// Hash ID
	hash.Write([]byte(r.ID))

	// Hash Name
	hash.Write([]byte(r.Name))

	// Hash Description
	hash.Write([]byte(r.Description))

	// Hash Enabled (nil treated as DB default true)
	if r.EnabledValue() {
		hash.Write([]byte("enabled:true"))
	} else {
		hash.Write([]byte("enabled:false"))
	}

	// Hash CelExpression
	hash.Write([]byte(r.CelExpression))

	// Hash Targets: sort by canonical marshaled payload for determinism, then hash each target as a single blob
	targets := make([]tables.TableRoutingTarget, len(r.Targets))
	copy(targets, r.Targets)
	sort.Slice(targets, func(i, j int) bool {
		pi := routingTargetHashPayload{Provider: derefStr(targets[i].Provider), Model: derefStr(targets[i].Model), KeyID: derefStr(targets[i].KeyID), Weight: targets[i].Weight}
		pj := routingTargetHashPayload{Provider: derefStr(targets[j].Provider), Model: derefStr(targets[j].Model), KeyID: derefStr(targets[j].KeyID), Weight: targets[j].Weight}
		di, err := sonic.Marshal(pi)
		if err != nil {
			return false
		}
		dj, err := sonic.Marshal(pj)
		if err != nil {
			return false
		}
		return string(di) < string(dj)
	})
	for _, t := range targets {
		payload := routingTargetHashPayload{Provider: derefStr(t.Provider), Model: derefStr(t.Model), KeyID: derefStr(t.KeyID), Weight: t.Weight}
		data, err := sonic.Marshal(payload)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash Fallbacks: use DB string when set, else marshal ParsedFallbacks (config-origin)
	if r.Fallbacks != nil {
		hash.Write([]byte(*r.Fallbacks))
	} else if len(r.ParsedFallbacks) > 0 {
		data, err := sonic.Marshal(r.ParsedFallbacks)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash Query: use raw string when set, else marshal ParsedQuery (config-origin)
	// Use OrderedMap's deterministic marshalling to ensure consistent hashes across runs
	if r.Query != nil {
		hash.Write([]byte(*r.Query))
	} else if len(r.ParsedQuery) > 0 {
		// Convert map to OrderedMap and use sorted marshalling for deterministic hashes
		orderedMap := schemas.OrderedMapFromMap(r.ParsedQuery)
		data, err := orderedMap.MarshalSorted()
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	// Hash ChainRule
	if r.ChainRule {
		hash.Write([]byte("chain_rule:true"))
	} else {
		hash.Write([]byte("chain_rule:false"))
	}

	// Hash Scope
	hash.Write([]byte(r.Scope))

	// Hash ScopeID (nil = global)
	scopeID := ""
	if r.ScopeID != nil {
		scopeID = *r.ScopeID
	}
	hash.Write([]byte(scopeID))

	// Hash Priority
	hash.Write([]byte(strconv.Itoa(r.Priority)))

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GeneratePricingOverrideHash generates a SHA256 hash for a pricing override.
// Skips: CreatedAt, UpdatedAt, ConfigHash (dynamic/meta fields).
func GeneratePricingOverrideHash(p tables.TablePricingOverride) (string, error) {
	hash := sha256.New()
	hash.Write([]byte(p.ID))
	hash.Write([]byte(p.Name))
	hash.Write([]byte(p.ScopeKind))
	hash.Write([]byte(derefStr(p.VirtualKeyID)))
	hash.Write([]byte(derefStr(p.ProviderID)))
	hash.Write([]byte(derefStr(p.ProviderKeyID)))
	hash.Write([]byte(p.MatchType))
	hash.Write([]byte(p.Pattern))
	hash.Write([]byte(p.RequestTypesJSON))
	hash.Write([]byte(p.PricingPatchJSON))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateMCPClientHash generates a SHA256 hash for an MCP client.
// This is used to detect changes to MCP clients between config.json and database.
// Skips: ID (autoIncrement), ClientID (system-assigned), CreatedAt, UpdatedAt (dynamic fields)
func GenerateMCPClientHash(m tables.TableMCPClient) (string, error) {
	hash := sha256.New()

	// Hash Name
	hash.Write([]byte(m.Name))

	// Hash ConnectionType
	hash.Write([]byte(m.ConnectionType))

	// Hash ConnectionString
	if m.ConnectionString != nil {
		if m.ConnectionString.IsFromSecret() {
			hash.Write([]byte(m.ConnectionString.GetRawRef()))
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

	// Hash TLSConfig
	if m.TLSConfig != nil {
		data, err := sonic.Marshal(m.TLSConfig)
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
			if val.IsFromSecret() {
				hash.Write([]byte(k + ":ref:" + val.GetRawRef()))
			} else {
				hash.Write([]byte(k + ":val:" + val.Val))
			}
		}
	}

	// will enable it in the future with a migration
	// hash.Write([]byte("disabled:" + strconv.FormatBool(m.Disabled)))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// GenerateWebhookEndpointHash generates a SHA256 hash of a webhook endpoint's
// declared fields. This is used to detect changes between config.json and
// database config. Operational fields (failure counters, timestamps) and the
// generated ID are excluded on purpose.
func GenerateWebhookEndpointHash(endpoint *tables.TableWebhookEndpoint) (string, error) {
	hash := sha256.New()

	hash.Write([]byte("name:" + endpoint.Name))
	hash.Write([]byte("url:" + endpoint.URL))

	// The signing secret is deliberately excluded: it is set once at creation
	// and rotates only through RotateWebhookEndpointSecret, so config-file
	// sync must not treat a changed webhooks[].secret as a mutation — doing so
	// would store the new hash while UpdateWebhookEndpoint leaves the credential
	// untouched, silently diverging the two.

	// Hash Events (sorted for deterministic hashing)
	if len(endpoint.Events) > 0 {
		sortedEvents := make([]string, 0, len(endpoint.Events))
		for _, event := range endpoint.Events {
			sortedEvents = append(sortedEvents, string(event))
		}
		sort.Strings(sortedEvents)
		data, err := sonic.Marshal(sortedEvents)
		if err != nil {
			return "", err
		}
		hash.Write(data)
	}

	hash.Write([]byte("includeResponse:" + strconv.FormatBool(endpoint.IncludeResponse)))
	hash.Write([]byte("allowPrivateNetwork:" + strconv.FormatBool(endpoint.AllowPrivateNetwork)))
	hash.Write([]byte("disabled:" + strconv.FormatBool(endpoint.Disabled)))

	hash.Write([]byte("maxRetries:" + strconv.Itoa(endpoint.MaxRetries)))
	hash.Write([]byte("retryBackoffInitialSeconds:" + strconv.Itoa(endpoint.RetryBackoffInitialSeconds)))
	hash.Write([]byte("retryBackoffMaxSeconds:" + strconv.Itoa(endpoint.RetryBackoffMaxSeconds)))
	hash.Write([]byte("attemptTimeoutSeconds:" + strconv.Itoa(endpoint.AttemptTimeoutSeconds)))
	hash.Write([]byte("maxResponsePayloadKBs:" + strconv.Itoa(endpoint.MaxResponsePayloadKBs)))
	hash.Write([]byte("maxConcurrentDeliveries:" + strconv.Itoa(endpoint.MaxConcurrentDeliveries)))

	// Hash Headers (sorted for deterministic hashing)
	if len(endpoint.Headers) > 0 {
		keys := make([]string, 0, len(endpoint.Headers))
		for k := range endpoint.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := endpoint.Headers[k]
			if val.IsFromSecret() {
				hash.Write([]byte(k + ":ref:" + val.GetRawRef()))
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

// frameworkConfigHashPayload holds the config.json-sourced fields used for hashing.
type frameworkConfigHashPayload struct {
	PricingURL          *string `json:"pricing_url"`
	ModelParametersURL  *string `json:"model_parameters_url"`
	PricingSyncInterval *int64  `json:"pricing_sync_interval"`
}

type frameworkConfigHashPayloadWithMCP struct {
	PricingURL             *string `json:"pricing_url"`
	ModelParametersURL     *string `json:"model_parameters_url"`
	PricingSyncInterval    *int64  `json:"pricing_sync_interval"`
	MCPLibraryURL          *string `json:"mcp_library_url"`
	MCPLibrarySyncInterval *int64  `json:"mcp_library_sync_interval"`
}

// FrameworkConfigHashOptions adds optional framework config fields to the
// config.json change-detection hash while preserving the legacy pricing-only
// hash when omitted.
type FrameworkConfigHashOptions struct {
	MCPLibraryURL          *string
	MCPLibrarySyncInterval *int64
}

// GenerateFrameworkConfigHash generates a SHA256 hash for a framework config.
// This is used to detect changes to framework config between config.json and database.
func GenerateFrameworkConfigHash(pricingURL *string, modelParametersURL *string, pricingSyncInterval *int64, opts ...FrameworkConfigHashOptions) (string, error) {
	var data []byte
	var err error
	if len(opts) > 0 {
		data, err = sonic.Marshal(frameworkConfigHashPayloadWithMCP{
			PricingURL:             pricingURL,
			ModelParametersURL:     modelParametersURL,
			PricingSyncInterval:    pricingSyncInterval,
			MCPLibraryURL:          opts[0].MCPLibraryURL,
			MCPLibrarySyncInterval: opts[0].MCPLibrarySyncInterval,
		})
	} else {
		data, err = sonic.Marshal(frameworkConfigHashPayload{
			PricingURL:          pricingURL,
			ModelParametersURL:  modelParametersURL,
			PricingSyncInterval: pricingSyncInterval,
		})
	}
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// AuthConfig represents configured auth config for Bifrost dashboard
type AuthConfig struct {
	AdminUserName *schemas.SecretVar `json:"admin_username"`
	AdminPassword *schemas.SecretVar `json:"admin_password"`
	IsEnabled     bool               `json:"is_enabled"`
}

// ConfigMap maps provider names to their configurations.
type ConfigMap map[schemas.ModelProvider]ProviderConfig

// GovernanceConfig contains governance entities loaded from the config store or
// reconciled from config.json.
type GovernanceConfig struct {
	VirtualKeys              []tables.TableVirtualKey      `json:"virtual_keys"`
	Teams                    []tables.TableTeam            `json:"teams"`
	Customers                []tables.TableCustomer        `json:"customers"`
	Budgets                  []tables.TableBudget          `json:"budgets"`
	RateLimits               []tables.TableRateLimit       `json:"rate_limits"`
	ModelConfigs             []tables.TableModelConfig     `json:"model_configs"`
	Providers                []tables.TableProvider        `json:"providers"`
	RoutingRules             []tables.TableRoutingRule     `json:"routing_rules"`
	PricingOverrides         []tables.TablePricingOverride `json:"pricing_overrides,omitempty"`
	AuthConfig               *AuthConfig                   `json:"auth_config,omitempty"`
	ComplexityAnalyzerConfig *ComplexityAnalyzerConfig     `json:"complexity_analyzer_config,omitempty"`
}
