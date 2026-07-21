package tables

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// DualCredentialConflictBehavior controls what happens on inference requests that
// carry both an IDP access token (Authorization: Bearer <jwt>) and a virtual key (x-bf-vk).
type DualCredentialConflictBehavior string

const (
	// DualCredentialConflictBehaviorError rejects the request with 400.
	DualCredentialConflictBehaviorError DualCredentialConflictBehavior = "error"
	// DualCredentialConflictBehaviorPreferVK drops the IDP token and authenticates via the virtual key.
	DualCredentialConflictBehaviorPreferVK DualCredentialConflictBehavior = "prefer_vk"
	// DualCredentialConflictBehaviorPreferIDP uses the IDP token for identity (default when unset).
	DualCredentialConflictBehaviorPreferIDP DualCredentialConflictBehavior = "prefer_idp"
)

// TableClientConfig represents global client configuration in the database
type TableClientConfig struct {
	ID                                    uint                           `gorm:"primaryKey;autoIncrement" json:"id"`
	DropExcessRequests                    bool                           `gorm:"default:false" json:"drop_excess_requests"`
	PrometheusLabelsJSON                  string                         `gorm:"type:text" json:"-"` // JSON serialized []string
	AllowedOriginsJSON                    string                         `gorm:"type:text" json:"-"` // JSON serialized []string
	AllowedHeadersJSON                    string                         `gorm:"type:text" json:"-"` // JSON serialized []string
	HeaderFilterConfigJSON                string                         `gorm:"type:text" json:"-"` // JSON serialized GlobalHeaderFilterConfig
	MetadataJSON                          string                         `gorm:"type:text" json:"-"` // JSON serialized map[string]any for UI/admin preferences (e.g. onboarding_dismissed). Bypasses config.json sync.
	InitialPoolSize                       int                            `gorm:"default:300" json:"initial_pool_size"`
	EnableLogging                         *bool                          `gorm:"default:true" json:"enable_logging"`
	DisableContentLogging                 bool                           `gorm:"default:false" json:"disable_content_logging"`          // DisableContentLogging controls whether sensitive content (inputs, outputs, embeddings, etc.) is logged
	RetainContentInObjectStorage          bool                           `gorm:"default:false" json:"retain_content_in_object_storage"` // When content logging is disabled, still offload content to object storage as hidden instead of dropping it
	DisableDBPingsInHealth                bool                           `gorm:"default:false" json:"disable_db_pings_in_health"`
	DumpErrorsInConsoleLogs               bool                           `gorm:"default:false" json:"dump_errors_in_console_logs"`       // Dump full error details to the server console logs
	LogRetentionDays                      int                            `gorm:"default:365" json:"log_retention_days" validate:"min=1"` // Number of days to retain logs (minimum 1 day)
	EnforceAuthOnInference                bool                           `gorm:"default:false" json:"enforce_auth_on_inference"`
	EnforceGovernanceHeader               bool                           `gorm:"" json:"enforce_governance_header"`
	EnforceSCIMAuth                       bool                           `gorm:"default:false" json:"enforce_scim_auth"`
	DualCredentialConflictBehavior        DualCredentialConflictBehavior `gorm:"column:dual_credential_conflict_behavior;type:varchar(20);not null;default:'prefer_idp'" json:"dual_credential_conflict_behavior"`
	MaxRequestBodySizeMB                  int                            `gorm:"default:100" json:"max_request_body_size_mb"`
	MCPAgentDepth                         int                            `gorm:"default:10" json:"mcp_agent_depth"`
	MCPToolExecutionTimeout               int                            `gorm:"default:30" json:"mcp_tool_execution_timeout"`                    // Timeout for individual tool execution in seconds (default: 30)
	MCPCodeModeBindingLevel               string                         `gorm:"default:server" json:"mcp_code_mode_binding_level"`               // How tools are exposed in VFS: "server" or "tool"
	MCPToolSyncInterval                   int                            `gorm:"default:10" json:"mcp_tool_sync_interval"`                        // Global tool sync interval in minutes (default: 10, 0 = disabled)
	MCPDisableAutoToolInject              bool                           `gorm:"default:false" json:"mcp_disable_auto_tool_inject"`               // When true, MCP tools are not injected into requests by default
	MCPEnableTempTokenAuth                bool                           `gorm:"default:false" json:"mcp_enable_temp_token_auth"`                 // When true, scoped temp tokens can authorize MCP per-user OAuth and per-user-headers auth pages. User-mode flows never mint regardless.
	AsyncJobResultTTL                     int                            `gorm:"default:3600" json:"async_job_result_ttl"`                        // Default TTL for async job results in seconds (default: 3600 = 1 hour)
	RequiredHeadersJSON                   string                         `gorm:"type:text" json:"-"`                                              // JSON serialized []string
	LoggingHeadersJSON                    string                         `gorm:"type:text" json:"-"`                                              // JSON serialized []string
	HideDeletedVirtualKeysInFilters       bool                           `gorm:"default:false" json:"hide_deleted_virtual_keys_in_filters"`       // Hide deleted virtual keys in logs filter dropdowns
	RoutingChainMaxDepth                  int                            `gorm:"default:10" json:"routing_chain_max_depth"`                       // Maximum depth for routing rule chain evaluation (default: 10)
	MCPExternalClientURL                  string                         `gorm:"type:varchar(512)" json:"mcp_external_client_url,omitempty"`      // Public base URL used as redirect_uri when Bifrost acts as an OAuth client to upstream MCP servers
	WhitelistedRoutesJSON                 string                         `gorm:"type:text" json:"-"`                                              // JSON serialized []string
	AllowPerRequestContentStorageOverride bool                           `gorm:"default:false" json:"allow_per_request_content_storage_override"` // Allow per-request override for content storage (e.g. long-term vs ephemeral)
	AllowPerRequestRawOverride            bool                           `gorm:"default:false" json:"allow_per_request_raw_override"`             // Allow per-request override for raw request/response storage
	AllowDirectKeys                       bool                           `gorm:"default:false" json:"allow_direct_keys"`                          // Allow callers to bypass the registered key pool via x-bf-direct-key header
	VKRotationCooldownNS                  int64                          `gorm:"column:vk_rotation_cooldown_ns;default:0" json:"-"`               // Rotation grace period in nanoseconds (Go duration encoding); 0 = previous value stops working immediately

	// Compat plugin feature flags
	CompatConvertTextToChat      bool `gorm:"column:compat_convert_text_to_chat;default:false" json:"-"`
	CompatConvertChatToResponses bool `gorm:"column:compat_convert_chat_to_responses;default:false" json:"-"`
	CompatShouldDropParams       bool `gorm:"column:compat_should_drop_params;default:false" json:"-"`
	CompatShouldConvertParams    bool `gorm:"column:compat_should_convert_params;default:false" json:"-"`

	// MCPServerAuthMode controls how /mcp authenticates inbound clients.
	// Stored as a plain varchar column so it can be read without JSON parsing.
	MCPServerAuthMode MCPServerAuthMode `gorm:"column:mcp_server_auth_mode;type:varchar(20);not null;default:'headers'" json:"mcp_server_auth_mode"`
	// OAuth2ServerConfigJSON holds the OAuth2 AS-specific settings (IssuerURL,
	// AuthCodeTTL, AccessTokenTTL) as a JSON blob. Only relevant when
	// MCPServerAuthMode is both or oauth. Deserialized into OAuth2ServerConfig
	// by AfterFind. The explicit column name avoids GORM deriving the leading
	// acronym as "o_auth2_..." from the field name.
	OAuth2ServerConfigJSON string `gorm:"column:oauth2_server_config_json;type:text" json:"-"`
	// WebhookConfigJSON holds the webhook delivery settings as a JSON blob,
	// deserialized into Webhooks by AfterFind.
	WebhookConfigJSON string `gorm:"column:webhook_config_json;type:text" json:"-"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`

	// Virtual fields for runtime use (not stored in DB)
	PrometheusLabels   []string                  `gorm:"-" json:"prometheus_labels"`
	AllowedOrigins     []string                  `gorm:"-" json:"allowed_origins,omitempty"`
	AllowedHeaders     []string                  `gorm:"-" json:"allowed_headers,omitempty"`
	RequiredHeaders    []string                  `gorm:"-" json:"required_headers,omitempty"`
	LoggingHeaders     []string                  `gorm:"-" json:"logging_headers,omitempty"`
	WhitelistedRoutes  []string                  `gorm:"-" json:"whitelisted_routes,omitempty"`
	HeaderFilterConfig *GlobalHeaderFilterConfig `gorm:"-" json:"header_filter_config,omitempty"`
	Metadata           map[string]any            `gorm:"-" json:"metadata,omitempty"`
	OAuth2ServerConfig *OAuth2ServerConfig       `gorm:"-" json:"oauth2_server_config,omitempty"`
	WebhookConfig      *WebhookConfig            `gorm:"-" json:"webhook_config,omitempty"`
}

// WebhookConfig holds global webhook delivery settings. Delivery
// tuning (retries, backoff, timeouts, payload caps) lives on each endpoint;
// only storage policy is global. Zero values fall back to the defaults,
// read at server startup.
type WebhookConfig struct {
	DeliveryHistoryRetentionDays int `json:"delivery_history_retention_days,omitempty"` // How long delivery history rows are kept (default: 30)
}

// DeliveryHistoryRetention returns the configured retention for webhook
// delivery history rows, falling back to the default (30 days) when unset.
// Safe on a nil receiver — nil means all defaults.
func (w *WebhookConfig) DeliveryHistoryRetention() time.Duration {
	if w == nil || w.DeliveryHistoryRetentionDays <= 0 {
		return 30 * 24 * time.Hour
	}
	return time.Duration(w.DeliveryHistoryRetentionDays) * 24 * time.Hour
}

// TableName sets the table name for each model
func (TableClientConfig) TableName() string { return "config_client" }

func (cc *TableClientConfig) BeforeSave(tx *gorm.DB) error {
	if cc.PrometheusLabels != nil {
		data, err := json.Marshal(cc.PrometheusLabels)
		if err != nil {
			return err
		}
		cc.PrometheusLabelsJSON = string(data)
	} else {
		cc.PrometheusLabelsJSON = "[]"
	}

	if cc.AllowedOrigins != nil {
		data, err := json.Marshal(cc.AllowedOrigins)
		if err != nil {
			return err
		}
		cc.AllowedOriginsJSON = string(data)
	} else {
		cc.AllowedOriginsJSON = "[]"
	}

	if cc.AllowedHeaders != nil {
		data, err := json.Marshal(cc.AllowedHeaders)
		if err != nil {
			return err
		}
		cc.AllowedHeadersJSON = string(data)
	} else {
		cc.AllowedHeadersJSON = "[]"
	}

	if cc.WhitelistedRoutes != nil {
		data, err := json.Marshal(cc.WhitelistedRoutes)
		if err != nil {
			return err
		}
		cc.WhitelistedRoutesJSON = string(data)
	} else {
		cc.WhitelistedRoutesJSON = "[]"
	}

	if cc.RequiredHeaders != nil {
		data, err := json.Marshal(cc.RequiredHeaders)
		if err != nil {
			return err
		}
		cc.RequiredHeadersJSON = string(data)
	} else {
		cc.RequiredHeadersJSON = "[]"
	}

	if cc.LoggingHeaders != nil {
		data, err := json.Marshal(cc.LoggingHeaders)
		if err != nil {
			return err
		}
		cc.LoggingHeadersJSON = string(data)
	} else {
		cc.LoggingHeadersJSON = "[]"
	}

	if cc.HeaderFilterConfig != nil {
		data, err := json.Marshal(cc.HeaderFilterConfig)
		if err != nil {
			return err
		}
		cc.HeaderFilterConfigJSON = string(data)
	} else {
		cc.HeaderFilterConfigJSON = ""
	}

	// Metadata is preserved when nil — callers that DELETE+CREATE through
	// UpdateClientConfig must carry MetadataJSON forward explicitly, since the
	// API ClientConfig does not expose Metadata. A nil Metadata here means
	// "leave whatever MetadataJSON the caller set untouched."
	if cc.Metadata != nil {
		data, err := json.Marshal(cc.Metadata)
		if err != nil {
			return err
		}
		cc.MetadataJSON = string(data)
	}

	if cc.OAuth2ServerConfig != nil {
		data, err := json.Marshal(cc.OAuth2ServerConfig)
		if err != nil {
			return err
		}
		cc.OAuth2ServerConfigJSON = string(data)
	} else {
		cc.OAuth2ServerConfigJSON = ""
	}

	if cc.WebhookConfig != nil {
		data, err := json.Marshal(cc.WebhookConfig)
		if err != nil {
			return err
		}
		cc.WebhookConfigJSON = string(data)
	} else {
		cc.WebhookConfigJSON = ""
	}

	return nil
}

// AfterFind hooks for deserialization
func (cc *TableClientConfig) AfterFind(tx *gorm.DB) error {
	if cc.PrometheusLabelsJSON != "" {
		if err := json.Unmarshal([]byte(cc.PrometheusLabelsJSON), &cc.PrometheusLabels); err != nil {
			return err
		}
	}

	if cc.AllowedOriginsJSON != "" {
		if err := json.Unmarshal([]byte(cc.AllowedOriginsJSON), &cc.AllowedOrigins); err != nil {
			return err
		}
	}

	if cc.AllowedHeadersJSON != "" {
		if err := json.Unmarshal([]byte(cc.AllowedHeadersJSON), &cc.AllowedHeaders); err != nil {
			return err
		}
	}

	if cc.WhitelistedRoutesJSON != "" {
		if err := json.Unmarshal([]byte(cc.WhitelistedRoutesJSON), &cc.WhitelistedRoutes); err != nil {
			return err
		}
	}

	if cc.RequiredHeadersJSON != "" {
		if err := json.Unmarshal([]byte(cc.RequiredHeadersJSON), &cc.RequiredHeaders); err != nil {
			return err
		}
	}

	if cc.LoggingHeadersJSON != "" {
		if err := json.Unmarshal([]byte(cc.LoggingHeadersJSON), &cc.LoggingHeaders); err != nil {
			return err
		}
	}

	if cc.HeaderFilterConfigJSON != "" {
		var headerFilterConfig GlobalHeaderFilterConfig
		if err := json.Unmarshal([]byte(cc.HeaderFilterConfigJSON), &headerFilterConfig); err != nil {
			return err
		}
		cc.HeaderFilterConfig = &headerFilterConfig
	}

	if cc.MetadataJSON != "" {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(cc.MetadataJSON), &metadata); err != nil {
			return err
		}
		cc.Metadata = metadata
	} else {
		cc.Metadata = nil
	}

	if cc.OAuth2ServerConfigJSON != "" {
		var authCfg OAuth2ServerConfig
		if err := json.Unmarshal([]byte(cc.OAuth2ServerConfigJSON), &authCfg); err != nil {
			return err
		}
		cc.OAuth2ServerConfig = &authCfg
	} else {
		cc.OAuth2ServerConfig = nil
	}

	if cc.WebhookConfigJSON != "" {
		var webhooksCfg WebhookConfig
		if err := json.Unmarshal([]byte(cc.WebhookConfigJSON), &webhooksCfg); err != nil {
			return err
		}
		cc.WebhookConfig = &webhooksCfg
	} else {
		cc.WebhookConfig = nil
	}

	return nil
}
