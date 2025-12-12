package tables

import "github.com/maximhq/bifrost/core/network"

const (
	ConfigAdminUsernameKey          = "admin_username"
	ConfigAdminPasswordKey          = "admin_password"
	ConfigIsAuthEnabledKey          = "is_auth_enabled"
	ConfigDisableAuthOnInferenceKey = "disable_auth_on_inference"
	ConfigProxyKey                  = "proxy_config"
)



// GlobalProxyConfig represents the global proxy configuration
type GlobalProxyConfig struct {
	Enabled       bool            `json:"enabled"`
	Type          network.GlobalProxyType `json:"type"`                     // "http", "socks5", "tcp"
	URL           string    `json:"url"`                      // Proxy URL (e.g., http://proxy.example.com:8080)
	Username      string    `json:"username,omitempty"`       // Optional authentication username
	Password      string    `json:"password,omitempty"`       // Optional authentication password
	NoProxy       string    `json:"no_proxy,omitempty"`       // Comma-separated list of hosts to bypass proxy
	Timeout       int       `json:"timeout,omitempty"`        // Connection timeout in seconds
	SkipTLSVerify bool      `json:"skip_tls_verify,omitempty"`// Skip TLS certificate verification
	// Entity enablement flags
	EnableForSCIM      bool `json:"enable_for_scim"`      // Enable proxy for SCIM requests (enterprise only)
	EnableForInference bool `json:"enable_for_inference"` // Enable proxy for inference requests
	EnableForAPI       bool `json:"enable_for_api"`       // Enable proxy for API requests
}

// TableGovernanceConfig represents generic configuration key-value pairs
type TableGovernanceConfig struct {
	Key   string `gorm:"primaryKey;type:varchar(255)" json:"key"`
	Value string `gorm:"type:text" json:"value"`
}

// TableName sets the table name for each model
func (TableGovernanceConfig) TableName() string { return "governance_config" }
