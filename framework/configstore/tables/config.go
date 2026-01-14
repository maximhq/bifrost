package tables

import "github.com/maximhq/bifrost/core/network"

const (
	ConfigAdminUsernameKey          = "admin_username"
	ConfigAdminPasswordKey          = "admin_password"
	ConfigIsAuthEnabledKey          = "is_auth_enabled"
	ConfigDisableAuthOnInferenceKey = "disable_auth_on_inference"
	ConfigProxyKey                  = "proxy_config"
	ConfigRestartRequiredKey        = "restart_required"
	ConfigHeaderFilterKey           = "header_filter_config"
)

// RestartRequiredConfig represents the restart required configuration
// This is set when a config change requires a server restart to take effect
type RestartRequiredConfig struct {
	Required bool   `json:"required"`
	Reason   string `json:"reason,omitempty"`
}

// GlobalProxyConfig represents the global proxy configuration
type GlobalProxyConfig struct {
	Enabled       bool                    `json:"enabled"`
	Type          network.GlobalProxyType `json:"type"`                      // "http", "socks5", "tcp"
	URL           string                  `json:"url"`                       // Proxy URL (e.g., http://proxy.example.com:8080)
	Username      string                  `json:"username,omitempty"`        // Optional authentication username
	Password      string                  `json:"password,omitempty"`        // Optional authentication password
	NoProxy       string                  `json:"no_proxy,omitempty"`        // Comma-separated list of hosts to bypass proxy
	Timeout       int                     `json:"timeout,omitempty"`         // Connection timeout in seconds
	SkipTLSVerify bool                    `json:"skip_tls_verify,omitempty"` // Skip TLS certificate verification
	// Entity enablement flags
	EnableForSCIM      bool `json:"enable_for_scim"`      // Enable proxy for SCIM requests (enterprise only)
	EnableForInference bool `json:"enable_for_inference"` // Enable proxy for inference requests
	EnableForAPI       bool `json:"enable_for_api"`       // Enable proxy for API requests
}

// GlobalHeaderFilterConfig represents global header filtering configuration
// for headers forwarded to LLM providers via the x-bf-eh-* prefix.
// Filter logic:
// - If allowlist is non-empty, only headers in the allowlist are forwarded
// - If denylist is non-empty, headers in the denylist are dropped
// - If both are non-empty, allowlist takes precedence first, then denylist filters the result
type GlobalHeaderFilterConfig struct {
	Allowlist []string `json:"allowlist,omitempty"` // If non-empty, only these headers are allowed
	Denylist  []string `json:"denylist,omitempty"`  // Headers to always block
}

// TableGovernanceConfig represents generic configuration key-value pairs
type TableGovernanceConfig struct {
	Key   string `gorm:"primaryKey;type:varchar(255)" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// TableName sets the table name for each model
func (TableGovernanceConfig) TableName() string { return "governance_config" }
